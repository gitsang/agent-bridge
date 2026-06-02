package claude

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gitsang/agent-bridge/internal/bridge"
	"github.com/gitsang/agent-bridge/internal/types"
)

const ClaudeProviderID = "claude"

type ProcessRequest struct {
	Command   string
	Args      []string
	Env       map[string]string
	Directory string
}

type Process interface {
	Stdout() io.Reader
	Wait() error
	Kill() error
}

type ProcessFactory func(ctx context.Context, request ProcessRequest) (Process, error)

type Option func(*Options)

type Options struct {
	Logger         *slog.Logger
	Command        string
	Args           []string
	Env            map[string]string
	Timeout        time.Duration
	ProcessFactory ProcessFactory
}

type Agent struct {
	logger  *slog.Logger
	command string
	args    []string
	env     map[string]string
	timeout time.Duration
	factory ProcessFactory

	mu       sync.Mutex
	sessions map[string]*sessionState
}

type sessionState struct {
	ID        string
	Title     string
	Directory string
	Model     types.ModelRef
	Turns     map[string]*turnState
	Running   bool
	Started   bool
	UpdatedAt time.Time
}

type turnState struct {
	ID          string
	SessionID   string
	UserContent string
	CompletedAt float64
	Answer      strings.Builder
	Model       types.ModelRef
	Err         error
}

func WithLogger(logger *slog.Logger) Option {
	return func(target *Options) { target.Logger = logger }
}

func WithCommand(command string, args ...string) Option {
	return func(target *Options) {
		target.Command = command
		target.Args = append([]string(nil), args...)
	}
}

func WithEnv(env map[string]string) Option {
	return func(target *Options) { target.Env = env }
}

func WithTimeout(timeout time.Duration) Option {
	return func(target *Options) {
		if timeout >= 0 {
			target.Timeout = timeout
		}
	}
}

func WithProcessFactory(factory ProcessFactory) Option {
	return func(target *Options) { target.ProcessFactory = factory }
}

func NewClient(options ...Option) *Agent {
	resolved := Options{
		Logger:  slog.Default(),
		Command: "claude",
		Args:    []string{"--bare", "-p", "--output-format", "stream-json", "--verbose"},
		Timeout: 30 * time.Minute,
	}
	for _, apply := range options {
		if apply == nil {
			continue
		}
		apply(&resolved)
	}
	if resolved.Logger == nil {
		resolved.Logger = slog.Default()
	}
	if strings.TrimSpace(resolved.Command) == "" {
		resolved.Command = "claude"
	}
	if len(resolved.Args) == 0 {
		resolved.Args = []string{"--bare", "-p", "--output-format", "stream-json", "--verbose"}
	}
	factory := resolved.ProcessFactory
	if factory == nil {
		factory = newProcess
	}
	return &Agent{
		logger:   resolved.Logger,
		command:  resolved.Command,
		args:     append([]string(nil), resolved.Args...),
		env:      copyStringMap(resolved.Env),
		timeout:  resolved.Timeout,
		factory:  factory,
		sessions: map[string]*sessionState{},
	}
}

func (c *Agent) ListModels(context.Context, string) ([]types.ModelInfo, error) {
	return []types.ModelInfo{
		{ModelRef: types.ModelRef{ProviderID: ClaudeProviderID, ModelID: "haiku"}, ProviderName: "Claude", ModelName: "Claude Haiku"},
		{ModelRef: types.ModelRef{ProviderID: ClaudeProviderID, ModelID: "opus"}, ProviderName: "Claude", ModelName: "Claude Opus"},
		{ModelRef: types.ModelRef{ProviderID: ClaudeProviderID, ModelID: "sonnet"}, ProviderName: "Claude", ModelName: "Claude Sonnet"},
	}, nil
}

func (c *Agent) ResolveModel(ctx context.Context, spec, directory string) (types.ModelRef, error) {
	resolvedModel := strings.TrimSpace(spec)
	if resolvedModel == "" {
		return types.ModelRef{ProviderID: ClaudeProviderID, ModelID: "sonnet"}, nil
	}
	if strings.Contains(resolvedModel, "/") {
		pair := strings.SplitN(resolvedModel, "/", 2)
		providerID := strings.TrimSpace(pair[0])
		modelID := strings.TrimSpace(pair[1])
		if modelID == "" {
			return types.ModelRef{}, fmt.Errorf("invalid model format: %s", resolvedModel)
		}
		if providerID != "" && !strings.EqualFold(providerID, ClaudeProviderID) {
			return types.ModelRef{}, fmt.Errorf("unsupported claude model provider: %s", providerID)
		}
		return types.ModelRef{ProviderID: ClaudeProviderID, ModelID: modelID}, nil
	}
	models, err := c.ListModels(ctx, directory)
	if err != nil {
		return types.ModelRef{}, err
	}
	for _, candidate := range models {
		if strings.EqualFold(candidate.ModelID, resolvedModel) {
			return candidate.ModelRef, nil
		}
	}
	return types.ModelRef{}, fmt.Errorf("model not found: %s", resolvedModel)
}

func (c *Agent) ListAgents(context.Context, string) ([]types.AgentInfo, error) {
	return []types.AgentInfo{{Name: "claude-code", Description: "Claude Code coding agent", Mode: "default"}}, nil
}

func (c *Agent) ListSessions(context.Context, string) ([]types.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sessions := make([]types.Session, 0, len(c.sessions))
	for _, state := range c.sessions {
		sessions = append(sessions, types.Session{
			ID:        state.ID,
			Title:     state.Title,
			Directory: state.Directory,
			UpdatedAt: state.UpdatedAt,
		})
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })
	return sessions, nil
}

func (c *Agent) ListAllSessions(ctx context.Context) ([]types.Session, error) {
	return c.ListSessions(ctx, "")
}

func (c *Agent) GetSession(_ context.Context, sessionID string) (*types.Session, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("claude session id is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.sessions[resolvedSessionID]
	if state == nil {
		return &types.Session{ID: resolvedSessionID}, nil
	}
	session := types.Session{
		ID:        state.ID,
		Title:     state.Title,
		Directory: state.Directory,
		UpdatedAt: state.UpdatedAt,
	}
	return &session, nil
}

func (c *Agent) CreateSession(_ context.Context, request types.CreateSessionRequest) (*types.Session, error) {
	sessionID, err := newSessionID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	session := &sessionState{
		ID:        sessionID,
		Title:     strings.TrimSpace(request.Title),
		Directory: strings.TrimSpace(request.Directory),
		Turns:     map[string]*turnState{},
		UpdatedAt: now,
	}
	c.mu.Lock()
	c.sessions[session.ID] = session
	c.mu.Unlock()
	return &types.Session{
		ID:        session.ID,
		Title:     session.Title,
		Directory: session.Directory,
		UpdatedAt: now,
	}, nil
}

func (c *Agent) GetMessages(_ context.Context, sessionID string) ([]types.Message, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("claude session id is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.sessions[resolvedSessionID]
	if state == nil {
		return nil, nil
	}
	turns := sortedTurns(state.Turns)
	messages := make([]types.Message, 0, len(turns)*2)
	for _, turn := range turns {
		messages = append(messages, types.Message{ID: turn.ID + ":user", SessionID: resolvedSessionID, Role: "user", Content: turn.UserContent, CompletedAt: turn.CompletedAt, Model: turn.Model})
		if strings.TrimSpace(turn.Answer.String()) != "" {
			messages = append(messages, types.Message{ID: turn.ID, SessionID: resolvedSessionID, Role: "assistant", Content: strings.TrimSpace(turn.Answer.String()), CompletedAt: turn.CompletedAt, Model: turn.Model})
		}
	}
	return messages, nil
}

func (c *Agent) GetLatestAssistantMessage(_ context.Context, sessionID string) (*types.Message, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("claude session id is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.sessions[resolvedSessionID]
	if state == nil {
		return nil, nil
	}
	var latest *types.Message
	for _, turn := range state.Turns {
		content := strings.TrimSpace(turn.Answer.String())
		if content == "" || turn.CompletedAt <= 0 {
			continue
		}
		if latest == nil || turn.CompletedAt > latest.CompletedAt {
			latest = &types.Message{ID: turn.ID, SessionID: resolvedSessionID, Role: "assistant", Content: content, CompletedAt: turn.CompletedAt, Model: turn.Model}
		}
	}
	return latest, nil
}

func (c *Agent) Prompt(ctx context.Context, sessionID string, prompt string, optfs ...types.PromptOptionFunc) (*types.PromptHandle, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("claude session id is required")
	}
	content := strings.TrimSpace(prompt)
	if content == "" {
		return nil, fmt.Errorf("message content is required")
	}
	options := types.PromptOptions{}
	for _, apply := range optfs {
		if apply != nil {
			apply(&options)
		}
	}

	c.mu.Lock()
	state := c.sessions[resolvedSessionID]
	if state == nil {
		state = &sessionState{ID: resolvedSessionID, Turns: map[string]*turnState{}, Started: true, UpdatedAt: time.Now()}
		c.sessions[resolvedSessionID] = state
	}
	if state.Running {
		c.mu.Unlock()
		return nil, fmt.Errorf("claude session is busy: %s", resolvedSessionID)
	}
	state.Running = true
	turnID := fmt.Sprintf("turn-%d", len(state.Turns)+1)
	model := options.Model
	if model.IsZero() && !state.Model.IsZero() {
		model = state.Model
	}
	turn := &turnState{ID: turnID, SessionID: resolvedSessionID, UserContent: content, Model: model}
	state.Turns[turnID] = turn
	firstPrompt := !state.Started
	if directory := strings.TrimSpace(options.Directory); directory != "" {
		state.Directory = directory
	}
	if !model.IsZero() {
		state.Model = model
	}
	directory := state.Directory
	c.mu.Unlock()

	doneCh := make(chan struct{})
	errCh := make(chan error, 1)
	go c.runPrompt(ctx, resolvedSessionID, turnID, content, directory, model, firstPrompt, doneCh, errCh)
	return types.NewPromptHandle(doneCh, errCh), nil
}

func (c *Agent) PollMessagesAfter(_ context.Context, sessionID string, afterCompletedAt float64, output types.MessageOutputOptions) ([]*types.Message, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("claude session id is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.sessions[resolvedSessionID]
	if state == nil {
		return nil, nil
	}
	turns := sortedTurns(state.Turns)
	messages := make([]*types.Message, 0, len(turns))
	for _, turn := range turns {
		if turn.CompletedAt <= afterCompletedAt {
			continue
		}
		content := turn.render(output)
		if strings.TrimSpace(content) == "" {
			continue
		}
		messages = append(messages, &types.Message{ID: turn.ID, SessionID: resolvedSessionID, Role: "assistant", Content: content, CompletedAt: turn.CompletedAt, Model: turn.Model})
	}
	return messages, nil
}

func (c *Agent) ListPendingPermissions(context.Context, string) ([]types.PermissionRequest, error) {
	return []types.PermissionRequest{}, nil
}

func (c *Agent) ReplyPermission(context.Context, string, string, types.PermissionReply) error {
	return types.ErrInteractionNoLongerPending
}

func (c *Agent) ListPendingQuestions(context.Context, string) ([]types.QuestionRequest, error) {
	return []types.QuestionRequest{}, nil
}

func (c *Agent) ReplyQuestion(context.Context, string, string, [][]string) error {
	return types.ErrInteractionNoLongerPending
}

func (c *Agent) RejectQuestion(context.Context, string, string) error {
	return types.ErrInteractionNoLongerPending
}

func (c *Agent) runPrompt(ctx context.Context, sessionID, turnID, content, directory string, model types.ModelRef, firstPrompt bool, doneCh chan struct{}, errCh chan error) {
	startedAt := time.Now()
	var finalErr error
	defer func() {
		attrs := []any{
			slog.String("agent_driver", "claude"),
			slog.String("session_id", sessionID),
			slog.String("turn_id", turnID),
			slog.Duration("duration", time.Since(startedAt)),
		}
		if finalErr != nil {
			attrs = append(attrs, slog.String("error", finalErr.Error()))
		}
		c.logger.Debug("claude prompt finished", attrs...)
	}()
	promptCtx := ctx
	var cancel context.CancelFunc
	if c.timeout > 0 {
		promptCtx, cancel = context.WithTimeout(ctx, c.timeout)
	}
	if cancel != nil {
		defer cancel()
	}

	request := ProcessRequest{
		Command:   c.command,
		Args:      c.buildArgs(sessionID, content, model, firstPrompt),
		Env:       copyStringMap(c.env),
		Directory: directory,
	}
	c.logger.Debug("claude process starting",
		slog.String("command", request.Command),
		slog.Any("args", request.Args),
		slog.String("directory", request.Directory),
	)
	process, err := c.factory(promptCtx, request)
	if err != nil {
		c.completeTurn(sessionID, turnID, err)
		c.finishPrompt(sessionID, false)
		finalErr = err
		errCh <- err
		return
	}
	readErr := c.readStream(process.Stdout(), sessionID, turnID)
	if readErr != nil && promptCtx.Err() == nil {
		c.logger.Debug("claude stream read error",
			slog.String("session_id", sessionID),
			slog.Any("error", readErr),
		)
		_ = process.Kill()
	}
	waitErr := process.Wait()
	if promptCtx.Err() != nil {
		_ = process.Kill()
		err := promptCtx.Err()
		c.completeTurn(sessionID, turnID, err)
		c.finishPrompt(sessionID, false)
		finalErr = err
		errCh <- err
		return
	}
	if readErr != nil {
		c.completeTurn(sessionID, turnID, readErr)
		c.finishPrompt(sessionID, false)
		finalErr = readErr
		errCh <- readErr
		return
	}
	if waitErr != nil {
		c.logger.Debug("claude process exit error",
			slog.String("session_id", sessionID),
			slog.Any("error", waitErr),
		)
		c.completeTurn(sessionID, turnID, waitErr)
		c.finishPrompt(sessionID, false)
		finalErr = waitErr
		errCh <- waitErr
		return
	}
	c.completeTurn(sessionID, turnID, nil)
	c.finishPrompt(sessionID, true)
	close(doneCh)
}

func (c *Agent) buildArgs(sessionID, content string, model types.ModelRef, firstPrompt bool) []string {
	args := append([]string(nil), c.args...)
	if strings.TrimSpace(sessionID) != "" {
		if firstPrompt {
			args = append(args, "--session-id", sessionID)
		} else {
			args = append(args, "--resume", sessionID)
		}
	}
	if !model.IsZero() {
		args = append(args, "--model", model.ModelID)
	}
	args = append(args, content)
	return args
}

func (c *Agent) readStream(stdout io.Reader, sessionID, turnID string) error {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		c.logger.Debug("claude stream line",
			slog.String("session_id", sessionID),
			slog.String("line", line),
		)
		var event claudeEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			c.logger.Debug("claude stream parse error",
				slog.String("session_id", sessionID),
				slog.String("line", line),
				slog.Any("error", err),
			)
			return err
		}
		if err := c.applyEvent(sessionID, turnID, event); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (c *Agent) applyEvent(sessionID, turnID string, event claudeEvent) error {
	if event.Type == "error" {
		message := "claude stream error"
		if event.Error != nil && strings.TrimSpace(event.Error.Message) != "" {
			message = strings.TrimSpace(event.Error.Message)
		}
		err := fmt.Errorf("claude error: %s", message)
		c.setTurnError(sessionID, turnID, err)
		return err
	}
	if event.Type == "system" && event.Subtype == "init" {
		c.updateTurnModel(sessionID, turnID, event.Model)
		return nil
	}
	if event.Type == "stream_event" {
		if event.Event != nil && event.Event.Delta != nil && event.Event.Delta.Type == "text_delta" {
			c.appendTurnText(sessionID, turnID, event.Event.Delta.Text)
		}
		return nil
	}
	if event.Type == "assistant" && event.Message != nil {
		c.updateTurnModel(sessionID, turnID, event.Message.Model)
		text := event.Message.text()
		if text != "" {
			c.appendTurnTextIfEmpty(sessionID, turnID, text)
		}
		return nil
	}
	if event.Type == "result" && event.Result != "" {
		c.appendTurnTextIfEmpty(sessionID, turnID, event.Result)
	}
	return nil
}

func (c *Agent) finishPrompt(sessionID string, success bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if state := c.sessions[sessionID]; state != nil {
		state.Running = false
		if success {
			state.Started = true
		}
	}
}

func (c *Agent) completeTurn(sessionID, turnID string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.sessions[sessionID]
	if state == nil {
		return
	}
	turn := state.Turns[turnID]
	if turn == nil {
		return
	}
	now := nowCompletedAt()
	turn.CompletedAt = now
	turn.Err = err
	state.UpdatedAt = time.Unix(0, int64(now*float64(time.Second)))
}

func (c *Agent) setTurnError(sessionID, turnID string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	turn := c.turnLocked(sessionID, turnID)
	if turn != nil {
		turn.Err = err
	}
}

func (c *Agent) appendTurnText(sessionID, turnID, text string) {
	if text == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	turn := c.turnLocked(sessionID, turnID)
	if turn != nil {
		turn.Answer.WriteString(text)
	}
}

func (c *Agent) appendTurnTextIfEmpty(sessionID, turnID, text string) {
	if text == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	turn := c.turnLocked(sessionID, turnID)
	if turn != nil && turn.Answer.Len() == 0 {
		turn.Answer.WriteString(text)
	}
}

func (c *Agent) updateTurnModel(sessionID, turnID, modelID string) {
	if strings.TrimSpace(modelID) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	turn := c.turnLocked(sessionID, turnID)
	if turn != nil {
		turn.Model = types.ModelRef{ProviderID: ClaudeProviderID, ModelID: strings.TrimSpace(modelID)}
	}
}

func (c *Agent) turnLocked(sessionID, turnID string) *turnState {
	state := c.sessions[sessionID]
	if state == nil {
		return nil
	}
	return state.Turns[turnID]
}

func (t *turnState) render(output types.MessageOutputOptions) string {
	builder := strings.Builder{}
	if output.Includes(types.MessageContentAnswer) && t.Answer.Len() > 0 {
		builder.WriteString("\n" + strings.TrimSpace(t.Answer.String()))
	}
	return strings.TrimSpace(builder.String())
}

type claudeEvent struct {
	Type      string              `json:"type"`
	Subtype   string              `json:"subtype"`
	SessionID string              `json:"session_id"`
	Model     string              `json:"model"`
	Result    string              `json:"result"`
	Message   *claudeMessage      `json:"message"`
	Event     *claudeStreamEvent  `json:"event"`
	Error     *claudeMessageError `json:"error"`
}

type claudeStreamEvent struct {
	Type  string       `json:"type"`
	Delta *claudeDelta `json:"delta"`
}

type claudeDelta struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type claudeMessage struct {
	ID      string               `json:"id"`
	Role    string               `json:"role"`
	Model   string               `json:"model"`
	Content []claudeMessageBlock `json:"content"`
}

type claudeMessageBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type claudeMessageError struct {
	Message string `json:"message"`
}

func (e *claudeMessageError) UnmarshalJSON(data []byte) error {
	var obj struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &obj); err == nil {
		e.Message = obj.Message
		return nil
	}

	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		e.Message = s
		return nil
	}

	return fmt.Errorf("cannot unmarshal error field: %s", string(data))
}

func (m claudeMessage) text() string {
	parts := make([]string, 0, len(m.Content))
	for _, block := range m.Content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, strings.TrimSpace(block.Text))
		}
	}
	return strings.Join(parts, "\n")
}

type process struct {
	cmd        *exec.Cmd
	stdout     io.Reader
	stderrDone <-chan struct{}
	stderrBuf  *strings.Builder
}

func newProcess(ctx context.Context, request ProcessRequest) (Process, error) {
	command := firstNonEmpty(request.Command, "claude")
	cmd := exec.CommandContext(ctx, command, request.Args...)
	if strings.TrimSpace(request.Directory) != "" {
		cmd.Dir = strings.TrimSpace(request.Directory)
	}
	if len(request.Env) > 0 {
		cmd.Env = os.Environ()
		for key, value := range request.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", key, value))
		}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	stderrBuf := &strings.Builder{}
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrBuf, stderr)
	}()
	return &process{cmd: cmd, stdout: stdout, stderrDone: stderrDone, stderrBuf: stderrBuf}, nil
}

func (p *process) Stdout() io.Reader { return p.stdout }

func (p *process) Wait() error {
	err := p.cmd.Wait()
	if p.stderrDone != nil {
		<-p.stderrDone
	}
	if err != nil && p.stderrBuf != nil && p.stderrBuf.Len() > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(p.stderrBuf.String()))
	}
	return err
}

func (p *process) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}

func sortedTurns(turns map[string]*turnState) []*turnState {
	result := make([]*turnState, 0, len(turns))
	for _, turn := range turns {
		result = append(result, turn)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return strings.Join([]string{
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	}, "-"), nil
}

func nowCompletedAt() float64 {
	return float64(time.Now().UnixNano()) / float64(time.Second)
}

func copyStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	return maps.Clone(source)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

var _ bridge.Agent = (*Agent)(nil)
