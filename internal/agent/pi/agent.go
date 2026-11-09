// Package pi drives the pi coding agent through its RPC mode: a long-lived
// `pi --mode rpc` child process speaking JSONL commands, responses, and session
// events on stdio.
package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gitsang/agent-bridge/internal/bridge"
	"github.com/gitsang/agent-bridge/internal/types"
)

// TransportFactory starts the RPC child process.
type TransportFactory func(ctx context.Context, directory string) (*processTransport, error)

type Option func(*Options)

type Options struct {
	Logger     *slog.Logger
	Command    string
	Args       []string
	Env        map[string]string
	Directory  string
	Timeout    time.Duration
	SessionDir string
	Factory    TransportFactory
}

type Agent struct {
	logger     *slog.Logger
	command    string
	args       []string
	env        map[string]string
	directory  string
	timeout    time.Duration
	sessionDir string
	factory    TransportFactory

	mu      sync.Mutex
	startMu sync.Mutex
	proc    *processTransport
	initErr error

	seq     atomic.Int64
	pending sync.Map // request id -> chan record

	settledMu      sync.Mutex
	settledWaiters []chan struct{}

	sessionsMu sync.Mutex
	sessions   map[string]*sessionState

	// pending dialogs requested by pi extensions
	dialogsMu sync.Mutex
	dialogs   map[string]pendingDialog
	dialogSeq atomic.Int64
}

type sessionState struct {
	ID        string
	Title     string
	Directory string

	mu     sync.Mutex
	model  types.ModelRef
	turns  []*turnState
	active *turnState
	seq    int64
	lastID int64
}

type turnState struct {
	ID          string
	SessionID   string
	UserContent string
	Model       types.ModelRef

	mu          sync.Mutex
	answer      strings.Builder
	reasoning   strings.Builder
	tools       strings.Builder
	patches     strings.Builder
	diagnostics strings.Builder
	toolCallIDs map[string]struct{}
	completedAt float64
	stopReason  string
	err         error

	done     chan struct{}
	doneOnce sync.Once
}

type pendingDialog struct {
	request    types.QuestionRequest
	dialogType string
	options    []string
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

func WithDirectory(directory string) Option {
	return func(target *Options) { target.Directory = directory }
}

func WithSessionDir(sessionDir string) Option {
	return func(target *Options) { target.SessionDir = sessionDir }
}

func WithTimeout(timeout time.Duration) Option {
	return func(target *Options) {
		if timeout >= 0 {
			target.Timeout = timeout
		}
	}
}

func WithTransportFactory(factory TransportFactory) Option {
	return func(target *Options) { target.Factory = factory }
}

func NewClient(options ...Option) *Agent {
	resolved := Options{
		Logger:  slog.Default(),
		Command: "pi",
		Args:    []string{"--mode", "rpc"},
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
		resolved.Command = "pi"
	}
	if len(resolved.Args) == 0 {
		resolved.Args = []string{"--mode", "rpc"}
	}

	agent := &Agent{
		logger:     resolved.Logger,
		command:    resolved.Command,
		args:       append([]string(nil), resolved.Args...),
		env:        resolved.Env,
		directory:  strings.TrimSpace(resolved.Directory),
		timeout:    resolved.Timeout,
		sessionDir: strings.TrimSpace(resolved.SessionDir),
		sessions:   map[string]*sessionState{},
		dialogs:    map[string]pendingDialog{},
	}
	if resolved.Factory != nil {
		agent.factory = resolved.Factory
	} else {
		command := agent.command
		args := append([]string(nil), agent.args...)
		env := agent.env
		logger := agent.logger
		agent.factory = func(_ context.Context, directory string) (*processTransport, error) {
			return newProcessTransport(logger, command, args, env, secondNonEmpty(directory, agent.directory))
		}
	}
	return agent
}

var _ bridge.Agent = (*Agent)(nil)

// Model discovery

func (c *Agent) ListModels(ctx context.Context, _ string) ([]types.ModelInfo, error) {
	var resp struct {
		Models []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Provider string `json:"provider"`
		} `json:"models"`
	}
	if err := c.call(ctx, "get_available_models", nil, &resp); err != nil {
		return nil, err
	}

	models := make([]types.ModelInfo, 0, len(resp.Models))
	for _, item := range resp.Models {
		providerID := strings.TrimSpace(item.Provider)
		modelID := strings.TrimSpace(item.ID)
		if providerID == "" || modelID == "" {
			continue
		}
		models = append(models, types.ModelInfo{
			ModelRef:     types.ModelRef{ProviderID: providerID, ModelID: modelID},
			ProviderName: providerID,
			ModelName:    firstNonEmpty(strings.TrimSpace(item.Name), modelID),
		})
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].ProviderID == models[j].ProviderID {
			return models[i].ModelID < models[j].ModelID
		}
		return models[i].ProviderID < models[j].ProviderID
	})
	return models, nil
}

func (c *Agent) ResolveModel(ctx context.Context, spec, directory string) (types.ModelRef, error) {
	resolvedSpec := strings.TrimSpace(spec)
	if resolvedSpec == "" {
		return types.ModelRef{}, fmt.Errorf("model is required")
	}
	// pi also accepts a :thinking suffix, which is not part of the model ID.
	resolvedSpec = strings.SplitN(resolvedSpec, ":", 2)[0]

	if strings.Contains(resolvedSpec, "/") {
		pair := strings.SplitN(resolvedSpec, "/", 2)
		providerID := strings.TrimSpace(pair[0])
		modelID := strings.TrimSpace(pair[1])
		if providerID == "" || modelID == "" {
			return types.ModelRef{}, fmt.Errorf("invalid model format: %s", resolvedSpec)
		}
		return types.ModelRef{ProviderID: providerID, ModelID: modelID}, nil
	}

	models, err := c.ListModels(ctx, directory)
	if err != nil {
		return types.ModelRef{}, err
	}
	matches := make([]types.ModelInfo, 0, 2)
	for _, candidate := range models {
		if strings.EqualFold(candidate.ModelID, resolvedSpec) {
			matches = append(matches, candidate)
		}
	}
	switch len(matches) {
	case 0:
		return types.ModelRef{}, fmt.Errorf("model not found: %s", resolvedSpec)
	case 1:
		return matches[0].ModelRef, nil
	default:
		return types.ModelRef{}, fmt.Errorf("ambiguous model %s, use provider/model", resolvedSpec)
	}
}

func (c *Agent) ListAgents(context.Context, string) ([]types.AgentInfo, error) {
	return []types.AgentInfo{{Name: "pi", Description: "pi coding agent", Mode: "default"}}, nil
}

// Sessions

func (c *Agent) ListSessions(ctx context.Context, directory string) ([]types.Session, error) {
	// pi keeps sessions in one store; the RPC surface has no list command, so
	// sessions the bridge created are reported from local state.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolvedDirectory := strings.TrimSpace(directory)

	c.sessionsMu.Lock()
	defer c.sessionsMu.Unlock()
	sessions := make([]types.Session, 0, len(c.sessions))
	for _, state := range c.sessions {
		if resolvedDirectory != "" && state.Directory != resolvedDirectory {
			continue
		}
		sessions = append(sessions, types.Session{ID: state.ID, Title: state.Title, Directory: state.Directory})
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })
	return sessions, nil
}

func (c *Agent) ListAllSessions(ctx context.Context) ([]types.Session, error) {
	return c.ListSessions(ctx, "")
}

func (c *Agent) GetSession(ctx context.Context, sessionID string) (*types.Session, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("pi session id is required")
	}
	c.sessionsMu.Lock()
	state := c.sessions[resolvedSessionID]
	c.sessionsMu.Unlock()
	if state == nil {
		return &types.Session{ID: resolvedSessionID}, nil
	}
	return &types.Session{ID: state.ID, Title: state.Title, Directory: state.Directory}, nil
}

// CreateSession starts a fresh pi session. pi names sessions itself, so the
// bridge's title is applied with set_session_name after the process reports
// which ID it chose.
func (c *Agent) CreateSession(ctx context.Context, request types.CreateSessionRequest) (*types.Session, error) {
	var state struct {
		SessionID   string `json:"sessionId"`
		SessionFile string `json:"sessionFile"`
		SessionName string `json:"sessionName"`
	}
	if err := c.call(ctx, "get_state", nil, &state); err != nil {
		return nil, err
	}
	resolvedSessionID := strings.TrimSpace(state.SessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("pi reported an empty session id")
	}

	directory := secondNonEmpty(strings.TrimSpace(request.Directory), c.directory)
	title := strings.TrimSpace(request.Title)
	if title != "" {
		if err := c.call(ctx, "set_session_name", map[string]any{"name": title}, nil); err != nil {
			c.logger.Debug("pi set_session_name failed", slog.String("error", err.Error()))
			title = ""
		}
	}

	c.sessionsMu.Lock()
	session := c.ensureSessionLocked(resolvedSessionID)
	session.Directory = directory
	if title != "" {
		session.Title = title
	}
	c.sessionsMu.Unlock()

	return &types.Session{ID: resolvedSessionID, Title: title, Directory: directory}, nil
}

func (c *Agent) Messages(ctx context.Context, sessionID string) ([]types.Message, error) {
	turns, err := c.sessionTurns(sessionID)
	if err != nil {
		return nil, err
	}
	messages := make([]types.Message, 0, len(turns)*2)
	for _, turn := range turns {
		messages = append(messages, types.Message{
			ID:        turn.ID + ":user",
			SessionID: turn.SessionID,
			Role:      "user",
			Content:   turn.UserContent,
			Model:     turn.Model,
		})
		if answer := turn.currentAnswer(); answer != "" {
			messages = append(messages, types.Message{
				ID:          turn.ID,
				SessionID:   turn.SessionID,
				Role:        "assistant",
				Content:     answer,
				CompletedAt: turn.currentCompletedAt(),
				Model:       turn.Model,
			})
		}
	}
	return messages, nil
}

func (c *Agent) GetMessages(ctx context.Context, sessionID string) ([]types.Message, error) {
	return c.Messages(ctx, sessionID)
}

func (c *Agent) GetLatestAssistantMessage(ctx context.Context, sessionID string) (*types.Message, error) {
	turns, err := c.sessionTurns(sessionID)
	if err != nil {
		return nil, err
	}
	var latest *types.Message
	for _, turn := range turns {
		answer := turn.currentAnswer()
		completedAt := turn.currentCompletedAt()
		if answer == "" || completedAt <= 0 {
			continue
		}
		if latest == nil || completedAt > latest.CompletedAt {
			latest = &types.Message{
				ID:          turn.ID,
				SessionID:   turn.SessionID,
				Role:        "assistant",
				Content:     answer,
				CompletedAt: completedAt,
				Model:       turn.Model,
			}
		}
	}
	return latest, nil
}

func (c *Agent) sessionTurns(sessionID string) ([]*turnState, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("pi session id is required")
	}
	c.sessionsMu.Lock()
	state := c.sessions[resolvedSessionID]
	c.sessionsMu.Unlock()
	if state == nil {
		return nil, nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return append([]*turnState(nil), state.turns...), nil
}

// Prompting

func (c *Agent) Prompt(ctx context.Context, sessionID string, prompt string, optfs ...types.PromptOptionFunc) (*types.PromptHandle, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("pi session id is required")
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

	c.sessionsMu.Lock()
	state := c.ensureSessionLocked(resolvedSessionID)
	c.sessionsMu.Unlock()

	state.mu.Lock()
	if !options.Model.IsZero() {
		state.model = options.Model
	}
	if directory := strings.TrimSpace(options.Directory); directory != "" {
		state.Directory = directory
	}
	state.seq++
	turn := &turnState{
		ID:          fmt.Sprintf("turn-%d", state.seq),
		SessionID:   resolvedSessionID,
		UserContent: content,
		Model:       state.model,
		done:        make(chan struct{}),
	}
	state.turns = append(state.turns, turn)
	state.active = turn
	state.mu.Unlock()

	if !options.Model.IsZero() {
		if err := c.call(ctx, "set_model", map[string]any{
			"provider": options.Model.ProviderID,
			"modelId":  options.Model.ModelID,
		}, nil); err != nil {
			c.logger.Debug("pi set_model failed",
				slog.String("model", options.Model.String()),
				slog.String("error", err.Error()),
			)
		}
	}

	doneCh := make(chan struct{})
	errCh := make(chan error, 1)
	go c.runPrompt(ctx, state, turn, content, doneCh, errCh)
	return types.NewPromptHandle(doneCh, errCh), nil
}

func (c *Agent) runPrompt(ctx context.Context, state *sessionState, turn *turnState, content string, doneCh chan struct{}, errCh chan error) {
	promptCtx := ctx
	var cancel context.CancelFunc
	if c.timeout > 0 {
		promptCtx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	settled := make(chan struct{})
	go c.waitSettled(promptCtx, settled)

	var resp struct {
		Disposition string `json:"disposition"`
	}
	err := c.call(promptCtx, "prompt", map[string]any{"message": content}, &resp)
	if err == nil && resp.Disposition == "handled" {
		// An extension consumed the prompt; no agent run will follow.
		turn.finish("handled", nil)
		state.mu.Lock()
		state.active = nil
		state.mu.Unlock()
		close(doneCh)
		return
	}
	if err != nil {
		if promptCtx.Err() != nil {
			c.abort()
			err = fmt.Errorf("pi prompt aborted: %w", promptCtx.Err())
		}
		turn.finish("", err)
		state.mu.Lock()
		state.active = nil
		state.mu.Unlock()
		errCh <- err
		return
	}

	select {
	case <-settled:
	case <-promptCtx.Done():
		c.abort()
		err = fmt.Errorf("pi prompt aborted: %w", promptCtx.Err())
		turn.finish("", err)
		state.mu.Lock()
		state.active = nil
		state.mu.Unlock()
		errCh <- err
		return
	}

	err = turn.currentError()
	turn.finish(turn.currentStopReason(), err)
	state.mu.Lock()
	state.active = nil
	state.mu.Unlock()
	if err != nil {
		errCh <- err
		return
	}
	close(doneCh)
}

// waitSettled closes done when pi signals that no automatic work remains.
func (c *Agent) waitSettled(ctx context.Context, done chan struct{}) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.settledSignal():
			close(done)
			return
		case <-ticker.C:
		}
	}
}

func (c *Agent) abort() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := c.call(ctx, "abort", nil, nil); err != nil {
		c.logger.Debug("pi abort failed", slog.String("error", err.Error()))
	}
}

func (c *Agent) PollMessagesAfter(_ context.Context, sessionID string, afterCompletedAt float64, output types.MessageOutputOptions) ([]*types.Message, error) {
	turns, err := c.sessionTurns(sessionID)
	if err != nil {
		return nil, err
	}
	messages := make([]*types.Message, 0, len(turns))
	for _, turn := range turns {
		completedAt := turn.currentCompletedAt()
		if completedAt <= afterCompletedAt {
			continue
		}
		answer, reasoning, tools, patches, diagnostics := turn.snapshot()
		if !output.Includes(types.MessageContentAnswer) {
			answer = ""
		}
		if !output.Includes(types.MessageContentReasoning) {
			reasoning = ""
		}
		if !output.Includes(types.MessageContentActionTool) {
			tools = ""
		}
		if !output.Includes(types.MessageContentArtifactPatch) {
			patches = ""
		}
		if !output.Includes(types.MessageContentDiagnostic) {
			diagnostics = ""
		}
		if answer == "" && reasoning == "" && tools == "" && patches == "" && diagnostics == "" {
			continue
		}
		messages = append(messages, &types.Message{
			ID:          turn.ID,
			SessionID:   turn.SessionID,
			Role:        "assistant",
			Content:     answer,
			Reasoning:   reasoning,
			Tools:       tools,
			Patches:     patches,
			Diagnostics: diagnostics,
			CompletedAt: completedAt,
			Model:       turn.Model,
		})
	}
	sort.Slice(messages, func(i, j int) bool { return messages[i].CompletedAt < messages[j].CompletedAt })
	return messages, nil
}

// Interactions

func (c *Agent) ListPendingPermissions(_ context.Context, _ string) ([]types.PermissionRequest, error) {
	// pi has no permission prompts: tool calls run with the permissions of the
	// process. Extensions that need confirmation use the dialog methods below.
	return []types.PermissionRequest{}, nil
}

func (c *Agent) ReplyPermission(context.Context, string, string, types.PermissionReply) error {
	return types.ErrInteractionNoLongerPending
}

func (c *Agent) ListPendingQuestions(_ context.Context, sessionID string) ([]types.QuestionRequest, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	c.dialogsMu.Lock()
	defer c.dialogsMu.Unlock()
	requests := make([]types.QuestionRequest, 0, len(c.dialogs))
	for _, dialog := range c.dialogs {
		if resolvedSessionID != "" && dialog.request.SessionID != resolvedSessionID {
			continue
		}
		requests = append(requests, dialog.request)
	}
	sort.Slice(requests, func(i, j int) bool { return requests[i].ID < requests[j].ID })
	return requests, nil
}

// ReplyQuestion answers an extension dialog (select, confirm, input, or editor).
func (c *Agent) ReplyQuestion(ctx context.Context, sessionID string, requestID string, answers [][]string) error {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return fmt.Errorf("pi session id is required")
	}
	dialog, err := c.takeDialog(strings.TrimSpace(requestID))
	if err != nil {
		return err
	}

	response := map[string]any{"type": "extension_ui_response", "id": dialog.request.ID}
	switch dialog.dialogType {
	case "confirm":
		value := firstAnswer(answers)
		response["confirmed"] = truthyAnswer(value, dialog.options)
	case "select":
		value := firstAnswer(answers)
		selected, ok := resolveSelectOption(value, dialog.options)
		if !ok {
			return fmt.Errorf("answer %q does not match any option", value)
		}
		response["value"] = selected
	default:
		value := firstAnswer(answers)
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("question answers are required")
		}
		response["value"] = value
	}
	return c.write(response)
}

func (c *Agent) RejectQuestion(ctx context.Context, sessionID string, requestID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("pi session id is required")
	}
	dialog, err := c.takeDialog(strings.TrimSpace(requestID))
	if err != nil {
		return err
	}
	return c.write(map[string]any{"type": "extension_ui_response", "id": dialog.request.ID, "cancelled": true})
}

func (c *Agent) takeDialog(requestID string) (pendingDialog, error) {
	if requestID == "" {
		return pendingDialog{}, fmt.Errorf("question request id is required")
	}
	c.dialogsMu.Lock()
	defer c.dialogsMu.Unlock()
	dialog, ok := c.dialogs[requestID]
	if !ok {
		return pendingDialog{}, types.ErrInteractionNoLongerPending
	}
	delete(c.dialogs, requestID)
	return dialog, nil
}

// RPC plumbing

func (c *Agent) ensureProcess(ctx context.Context) (*processTransport, error) {
	c.mu.Lock()
	if c.proc != nil {
		proc := c.proc
		c.mu.Unlock()
		return proc, nil
	}
	storedErr := c.initErr
	c.mu.Unlock()

	c.startMu.Lock()
	defer c.startMu.Unlock()

	c.mu.Lock()
	if c.proc != nil {
		proc := c.proc
		c.mu.Unlock()
		return proc, nil
	}
	c.mu.Unlock()

	if storedErr != nil {
		return nil, storedErr
	}

	proc, err := c.factory(ctx, c.directory)
	if err != nil {
		c.mu.Lock()
		c.initErr = err
		c.mu.Unlock()
		return nil, err
	}

	c.mu.Lock()
	c.proc = proc
	c.mu.Unlock()

	go c.readRecords(proc)
	return proc, nil
}

// readRecords dispatches every record pi writes until the process exits.
func (c *Agent) readRecords(proc *processTransport) {
	for {
		rec, err := proc.read()
		if err != nil {
			c.failPending(err)
			return
		}
		c.dispatch(rec)
	}
}

func (c *Agent) dispatch(rec record) {
	switch rec.Type {
	case "response":
		if ch, ok := c.pending.LoadAndDelete(rec.ID); ok {
			if waiter, ok := ch.(chan record); ok {
				select {
				case waiter <- rec:
				default:
				}
			}
		}
	case "extension_ui_request":
		c.handleDialogRequest(rec)
	case "agent_settled":
		c.settledMu.Lock()
		settled := c.settledWaiters
		c.settledWaiters = nil
		c.settledMu.Unlock()
		for _, waiter := range settled {
			close(waiter)
		}
	default:
		c.handleSessionEvent(rec)
	}
}

func (c *Agent) call(ctx context.Context, command string, fields map[string]any, target any) error {
	proc, err := c.ensureProcess(ctx)
	if err != nil {
		return err
	}

	id := fmt.Sprintf("req-%d", c.seq.Add(1))
	waiter := make(chan record, 1)
	c.pending.Store(id, waiter)

	payload := map[string]any{"type": command, "id": id}
	for key, value := range fields {
		payload[key] = value
	}
	if err := proc.write(payload); err != nil {
		c.pending.Delete(id)
		return err
	}

	select {
	case rec := <-waiter:
		if !rec.Success {
			return errors.New(firstNonEmpty(rec.Error, "pi command failed"))
		}
		if target != nil && len(rec.Data) > 0 {
			return json.Unmarshal(rec.Data, target)
		}
		return nil
	case <-ctx.Done():
		c.pending.Delete(id)
		return ctx.Err()
	}
}

func (c *Agent) write(payload any) error {
	proc, err := c.ensureProcess(context.Background())
	if err != nil {
		return err
	}
	return proc.write(payload)
}

func (c *Agent) failPending(err error) {
	c.pending.Range(func(key, value any) bool {
		c.pending.Delete(key)
		if waiter, ok := value.(chan record); ok {
			select {
			case waiter <- record{Success: false, Error: err.Error()}:
			default:
			}
		}
		return true
	})
}

// settledSignal returns a channel closed on the next agent_settled.
func (c *Agent) settledSignal() <-chan struct{} {
	ch := make(chan struct{})
	c.settledMu.Lock()
	c.settledWaiters = append(c.settledWaiters, ch)
	c.settledMu.Unlock()
	return ch
}

func (c *Agent) ensureSessionLocked(sessionID string) *sessionState {
	state := c.sessions[sessionID]
	if state == nil {
		state = &sessionState{ID: sessionID, turns: []*turnState{}}
		c.sessions[sessionID] = state
	}
	return state
}

func secondNonEmpty(primary, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return strings.TrimSpace(primary)
	}
	return strings.TrimSpace(fallback)
}

func firstAnswer(answers [][]string) string {
	for _, answerSet := range answers {
		for _, answer := range answerSet {
			if trimmed := strings.TrimSpace(answer); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func truthyAnswer(value string, options []string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "yes", "y", "true", "1", "confirm", "ok", "allow", "accept":
		return true
	case "no", "n", "false", "0", "cancel", "deny", "reject":
		return false
	}
	for index, option := range options {
		if strings.EqualFold(option, value) {
			// Confirm dialogs report their choices as a two-item list.
			return index == 0
		}
	}
	return false
}

func resolveSelectOption(value string, options []string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", false
	}
	for _, option := range options {
		if strings.EqualFold(option, trimmed) {
			return option, true
		}
	}
	// Allow picking by 1-based index.
	if index, err := parsePositiveInt(trimmed); err == nil && index >= 1 && index <= len(options) {
		return options[index-1], true
	}
	return "", false
}

func parsePositiveInt(value string) (int, error) {
	var parsed int
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil {
		return 0, err
	}
	if parsed <= 0 {
		return 0, errors.New("not a positive integer")
	}
	return parsed, nil
}
