package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gitsang/agent-bridge/internal/bridge"
	"github.com/gitsang/agent-bridge/internal/types"
)

// ProviderID labels models the agent exposes without a provider prefix.
const ProviderID = "acp"

// TransportFactory starts the JSON-RPC channel to an ACP agent.
type TransportFactory func(ctx context.Context, directory string) (Transport, error)

type Option func(*Options)

type Options struct {
	Logger            *slog.Logger
	Command           string
	Args              []string
	Env               map[string]string
	Directory         string
	Timeout           time.Duration
	InitializeTimeout time.Duration
	TransportFactory  TransportFactory
}

type Agent struct {
	logger            *slog.Logger
	command           string
	args              []string
	env               map[string]string
	directory         string
	timeout           time.Duration
	initializeTimeout time.Duration
	factory           TransportFactory

	mu       sync.Mutex
	startMu  sync.Mutex
	conn     *conn
	initErr  error
	caps     agentCapabilities
	sessions map[string]*sessionState
	pending  map[string]pendingPermission
	models   map[string]cachedModels
	permSeq  int64
}

// modelChoice is one model an agent advertised in its session config options.
// Value is kept verbatim because agents disagree on the encoding: some accept
// "provider/model", others only the exact string they offered.
type modelChoice struct {
	Value        string
	Ref          types.ModelRef
	Name         string
	ProviderName string
}

type cachedModels struct {
	options []configOption
	at      time.Time
}

// modelsCacheTTL bounds how often the driver spawns a discovery session.
const modelsCacheTTL = 5 * time.Minute

type sessionState struct {
	ID        string
	Title     string
	Directory string
	Model     types.ModelRef

	mu      sync.Mutex
	busy    bool
	turns   []*turnState
	active  *turnState
	seq     int64
	options []configOption
	choices []modelChoice
	helpers map[string]*toolCallState
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
	diagnostics strings.Builder
	completedAt float64
	stopReason  string
	err         error

	done     chan struct{}
	doneOnce sync.Once
}

type toolCallState struct {
	Name   string
	Input  string
	Output string
}

type pendingPermission struct {
	request types.PermissionRequest
	rpcID   json.RawMessage
	options map[string]string // optionId -> option kind
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

func WithTimeout(timeout time.Duration) Option {
	return func(target *Options) {
		if timeout >= 0 {
			target.Timeout = timeout
		}
	}
}

func WithInitializeTimeout(timeout time.Duration) Option {
	return func(target *Options) {
		if timeout >= 0 {
			target.InitializeTimeout = timeout
		}
	}
}

func WithTransportFactory(factory TransportFactory) Option {
	return func(target *Options) { target.TransportFactory = factory }
}

func NewClient(options ...Option) *Agent {
	resolved := Options{
		Logger:            slog.Default(),
		Timeout:           30 * time.Minute,
		InitializeTimeout: 30 * time.Second,
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

	agent := &Agent{
		logger:            resolved.Logger,
		command:           resolved.Command,
		args:              append([]string(nil), resolved.Args...),
		env:               resolved.Env,
		directory:         strings.TrimSpace(resolved.Directory),
		timeout:           resolved.Timeout,
		initializeTimeout: resolved.InitializeTimeout,
		factory:           resolved.TransportFactory,
		sessions:          map[string]*sessionState{},
		pending:           map[string]pendingPermission{},
		models:            map[string]cachedModels{},
	}
	if agent.factory == nil {
		command := agent.command
		args := append([]string(nil), agent.args...)
		env := agent.env
		logger := agent.logger
		agent.factory = func(ctx context.Context, directory string) (Transport, error) {
			return newProcessTransport(ctx, logger, command, args, env, directory)
		}
	}
	return agent
}

var _ bridge.Agent = (*Agent)(nil)

func (c *Agent) ListModels(ctx context.Context, directory string) ([]types.ModelInfo, error) {
	options, err := c.modelOptions(ctx, strings.TrimSpace(directory))
	if err != nil {
		return nil, err
	}

	models := make([]types.ModelInfo, 0)
	for _, option := range options {
		if option.ID != "model" && option.Category != "model" {
			continue
		}
		models = append(models, modelsFromConfigOption(option)...)
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].ProviderID == models[j].ProviderID {
			return models[i].ModelID < models[j].ModelID
		}
		return models[i].ProviderID < models[j].ProviderID
	})
	return models, nil
}

// modelOptions returns the session config options that advertise models. ACP
// has no global model catalog, so the driver asks for a throwaway session per
// directory and caches the answer.
func (c *Agent) modelOptions(ctx context.Context, directory string) ([]configOption, error) {
	resolvedDirectory := firstNonEmpty(directory, c.directory)

	c.mu.Lock()
	cached, ok := c.models[resolvedDirectory]
	c.mu.Unlock()
	if ok && time.Since(cached.at) < modelsCacheTTL {
		return cached.options, nil
	}

	session, err := c.CreateSession(ctx, types.CreateSessionRequest{Directory: resolvedDirectory})
	if err != nil {
		if ok {
			c.logger.Debug("acp model discovery failed, using cache",
				slog.String("directory", resolvedDirectory),
				slog.String("error", err.Error()),
			)
			return cached.options, nil
		}
		return nil, err
	}

	options := c.sessionOptions(session.ID)
	c.mu.Lock()
	c.models[resolvedDirectory] = cachedModels{options: options, at: time.Now()}
	c.mu.Unlock()

	_ = c.closeSession(context.WithoutCancel(ctx), session.ID)
	return options, nil
}

func choicesFromConfigOption(option configOption) []modelChoice {
	choices := make([]modelChoice, 0, len(option.Options))
	for _, raw := range option.Options {
		switch value := raw.(type) {
		case string:
			ref := parseModelValue(value)
			if ref.IsZero() {
				continue
			}
			choices = append(choices, modelChoice{Value: value, Ref: ref, Name: ref.ModelID, ProviderName: ref.ProviderID})
		case map[string]any:
			if nested, ok := value["options"].([]any); ok {
				choices = append(choices, choicesFromConfigOption(configOption{Options: nested})...)
				continue
			}
			rawValue, _ := value["value"].(string)
			ref := parseModelValue(rawValue)
			if ref.IsZero() {
				continue
			}
			name, _ := value["name"].(string)
			if strings.TrimSpace(name) == "" {
				name = ref.ModelID
			}
			choices = append(choices, modelChoice{Value: rawValue, Ref: ref, Name: strings.TrimSpace(name), ProviderName: ref.ProviderID})
		}
	}
	return choices
}

func modelsFromConfigOption(option configOption) []types.ModelInfo {
	choices := choicesFromConfigOption(option)
	models := make([]types.ModelInfo, 0, len(choices))
	for _, choice := range choices {
		models = append(models, types.ModelInfo{ModelRef: choice.Ref, ProviderName: choice.ProviderName, ModelName: choice.Name})
	}
	return models
}

func modelChoices(options []configOption) []modelChoice {
	for _, option := range options {
		if option.ID != "model" && option.Category != "model" {
			continue
		}
		return choicesFromConfigOption(option)
	}
	return nil
}

// modelValue returns the exact config value the agent offered for ref, falling
// back to the provider/model form when the agent never advertised it.
func modelValue(choices []modelChoice, ref types.ModelRef) string {
	for _, choice := range choices {
		if choice.Ref == ref {
			return choice.Value
		}
	}
	return ""
}

// parseModelValue reads the value form agents use for model config options:
// "provider/model", "provider/model:variant", a bare "model", or the JSON pair
// ["provider", "model"].
func parseModelValue(raw string) types.ModelRef {
	value := strings.TrimSpace(raw)
	if value == "" {
		return types.ModelRef{}
	}
	if strings.HasPrefix(value, "[") {
		var pair []string
		if err := json.Unmarshal([]byte(value), &pair); err == nil && len(pair) >= 2 {
			return types.ModelRef{
				ProviderID: strings.TrimSpace(pair[0]),
				ModelID:    strings.TrimSpace(pair[1]),
			}
		}
	}
	if index := strings.LastIndex(value, "/"); index > 0 {
		return types.ModelRef{
			ProviderID: strings.TrimSpace(value[:index]),
			ModelID:    strings.TrimSpace(value[index+1:]),
		}
	}
	return types.ModelRef{ProviderID: ProviderID, ModelID: value}
}

func (c *Agent) ResolveModel(_ context.Context, spec, _ string) (types.ModelRef, error) {
	resolvedSpec := strings.TrimSpace(spec)
	if resolvedSpec == "" {
		return types.ModelRef{}, fmt.Errorf("model is required")
	}
	if strings.Contains(resolvedSpec, "/") {
		ref := parseModelValue(resolvedSpec)
		if ref.IsZero() || ref.ProviderID == "" || ref.ModelID == "" {
			return types.ModelRef{}, fmt.Errorf("invalid model format: %s", resolvedSpec)
		}
		return ref, nil
	}
	return types.ModelRef{ProviderID: ProviderID, ModelID: resolvedSpec}, nil
}

func (c *Agent) ListAgents(context.Context, string) ([]types.AgentInfo, error) {
	return []types.AgentInfo{{Name: ProviderID, Description: "ACP agent", Mode: "default"}}, nil
}

func (c *Agent) ListSessions(ctx context.Context, directory string) ([]types.Session, error) {
	client, err := c.ensureConn(ctx)
	if err != nil {
		return nil, err
	}
	if !c.capabilities().SessionCapabilities.List.enabled() {
		return c.localSessions(directory), nil
	}

	params := listSessionsRequest{CWD: strings.TrimSpace(directory)}
	var resp listSessionsResponse
	if err := client.call(ctx, "session/list", params, &resp); err != nil {
		return nil, err
	}

	sessions := make([]types.Session, 0, len(resp.Sessions))
	for _, item := range resp.Sessions {
		session := types.Session{
			ID:        strings.TrimSpace(item.SessionID),
			Title:     strings.TrimSpace(item.Title),
			Directory: strings.TrimSpace(item.CWD),
		}
		if session.ID == "" {
			continue
		}
		if updatedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(item.UpdatedAt)); err == nil {
			session.UpdatedAt = updatedAt
		}
		c.storeSession(session)
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].UpdatedAt.Equal(sessions[j].UpdatedAt) {
			return sessions[i].ID < sessions[j].ID
		}
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})
	return sessions, nil
}

func (c *Agent) ListAllSessions(ctx context.Context) ([]types.Session, error) {
	return c.ListSessions(ctx, "")
}

func (c *Agent) localSessions(directory string) []types.Session {
	resolvedDirectory := strings.TrimSpace(directory)
	c.mu.Lock()
	defer c.mu.Unlock()
	sessions := make([]types.Session, 0, len(c.sessions))
	for _, state := range c.sessions {
		if resolvedDirectory != "" && state.Directory != resolvedDirectory {
			continue
		}
		sessions = append(sessions, types.Session{ID: state.ID, Title: state.Title, Directory: state.Directory})
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })
	return sessions
}

func (c *Agent) GetSession(_ context.Context, sessionID string) (*types.Session, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("acp session id is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.sessions[resolvedSessionID]
	if state == nil {
		return &types.Session{ID: resolvedSessionID}, nil
	}
	return &types.Session{ID: state.ID, Title: state.Title, Directory: state.Directory}, nil
}

func (c *Agent) CreateSession(ctx context.Context, request types.CreateSessionRequest) (*types.Session, error) {
	client, err := c.ensureConn(ctx)
	if err != nil {
		return nil, err
	}
	directory := firstNonEmpty(strings.TrimSpace(request.Directory), c.directory)
	if directory == "" {
		directory = "."
	}

	var resp newSessionResponse
	params := newSessionRequest{CWD: directory, MCPServers: []any{}}
	if err := client.call(ctx, "session/new", params, &resp); err != nil {
		return nil, err
	}
	resolvedSessionID := strings.TrimSpace(resp.SessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("acp session/new returned an empty session id")
	}

	title := strings.TrimSpace(request.Title)
	c.mu.Lock()
	state := c.ensureSessionLocked(resolvedSessionID)
	state.Directory = directory
	state.options = resp.ConfigOptions
	if title != "" {
		state.Title = title
	}
	if model := modelFromOptions(resp.ConfigOptions); !model.IsZero() {
		state.Model = model
	}
	state.choices = modelChoices(resp.ConfigOptions)
	c.mu.Unlock()

	return &types.Session{ID: resolvedSessionID, Title: title, Directory: directory}, nil
}

func (c *Agent) GetMessages(_ context.Context, sessionID string) ([]types.Message, error) {
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

func (c *Agent) GetLatestAssistantMessage(_ context.Context, sessionID string) (*types.Message, error) {
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
		return nil, fmt.Errorf("acp session id is required")
	}
	c.mu.Lock()
	state := c.sessions[resolvedSessionID]
	c.mu.Unlock()
	if state == nil {
		return nil, nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return append([]*turnState(nil), state.turns...), nil
}

func (c *Agent) Prompt(ctx context.Context, sessionID string, prompt string, optfs ...types.PromptOptionFunc) (*types.PromptHandle, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("acp session id is required")
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

	client, err := c.ensureConn(ctx)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	state := c.ensureSessionLocked(resolvedSessionID)
	c.mu.Unlock()

	state.mu.Lock()
	if state.busy {
		state.mu.Unlock()
		return nil, fmt.Errorf("acp session is busy: %s", resolvedSessionID)
	}
	if !options.Model.IsZero() {
		state.Model = options.Model
	}
	if directory := strings.TrimSpace(options.Directory); directory != "" {
		state.Directory = directory
	}
	state.seq++
	turn := &turnState{
		ID:          fmt.Sprintf("turn-%d", state.seq),
		SessionID:   resolvedSessionID,
		UserContent: content,
		Model:       state.Model,
		done:        make(chan struct{}),
	}
	state.turns = append(state.turns, turn)
	state.active = turn
	state.busy = true
	state.mu.Unlock()

	if !options.Model.IsZero() {
		c.applyModel(ctx, state, options.Model)
	}

	doneCh := make(chan struct{})
	errCh := make(chan error, 1)
	go c.runPrompt(ctx, client, state, turn, content, doneCh, errCh)
	return types.NewPromptHandle(doneCh, errCh), nil
}

func (c *Agent) applyModel(ctx context.Context, state *sessionState, model types.ModelRef) {
	state.mu.Lock()
	choices := append([]modelChoice(nil), state.choices...)
	state.mu.Unlock()

	value := modelValue(choices, model)
	if value == "" {
		value = strings.TrimSpace(model.String())
		if strings.EqualFold(model.ProviderID, ProviderID) {
			value = model.ModelID
		}
	}

	var resp setConfigOptionResponse
	err := c.callWithConn(ctx, "session/set_config_option", setConfigOptionRequest{
		SessionID: state.ID,
		ConfigID:  "model",
		Value:     value,
	}, &resp)
	if err != nil {
		c.logger.Debug("acp set model failed",
			slog.String("session_id", state.ID),
			slog.String("model", value),
			slog.String("error", err.Error()),
		)
		return
	}
	if len(resp.ConfigOptions) > 0 {
		c.mu.Lock()
		state.options = resp.ConfigOptions
		c.mu.Unlock()
	} else {
		c.mu.Lock()
		state.Model = model
		c.mu.Unlock()
	}
}

func (c *Agent) runPrompt(ctx context.Context, client *conn, state *sessionState, turn *turnState, content string, doneCh chan struct{}, errCh chan error) {
	promptCtx := ctx
	var cancel context.CancelFunc
	if c.timeout > 0 {
		promptCtx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	params := promptRequest{SessionID: state.ID, Prompt: []contentBlock{textBlock(content)}}
	var resp promptResponse
	err := client.call(promptCtx, "session/prompt", params, &resp)

	if promptCtx.Err() != nil {
		// The caller gave up: tell the agent to stop even after this returns.
		notifyCtx, notifyCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		if notifyErr := client.notify(notifyCtx, "session/cancel", cancelNotification{SessionID: state.ID}); notifyErr != nil {
			c.logger.Debug("acp session cancel failed",
				slog.String("session_id", state.ID),
				slog.String("error", notifyErr.Error()),
			)
		}
		notifyCancel()
		err = fmt.Errorf("acp prompt aborted: %w", promptCtx.Err())
	}

	turn.finish(strings.TrimSpace(resp.StopReason), err)

	state.mu.Lock()
	state.busy = false
	if state.active == turn {
		state.active = nil
	}
	state.mu.Unlock()

	if err != nil {
		errCh <- err
		return
	}
	close(doneCh)
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
		answer, reasoning, tools, diagnostics := turn.snapshot()
		if !output.Includes(types.MessageContentAnswer) {
			answer = ""
		}
		if !output.Includes(types.MessageContentReasoning) {
			reasoning = ""
		}
		if !output.Includes(types.MessageContentActionTool) {
			tools = ""
		}
		if !output.Includes(types.MessageContentDiagnostic) {
			diagnostics = ""
		}
		if answer == "" && reasoning == "" && tools == "" && diagnostics == "" {
			continue
		}
		messages = append(messages, &types.Message{
			ID:          turn.ID,
			SessionID:   turn.SessionID,
			Role:        "assistant",
			Content:     answer,
			Reasoning:   reasoning,
			Tools:       tools,
			Diagnostics: diagnostics,
			CompletedAt: completedAt,
			Model:       turn.Model,
		})
	}
	sort.Slice(messages, func(i, j int) bool { return messages[i].CompletedAt < messages[j].CompletedAt })
	return messages, nil
}

func (c *Agent) ListPendingPermissions(_ context.Context, sessionID string) ([]types.PermissionRequest, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	c.mu.Lock()
	defer c.mu.Unlock()
	requests := make([]types.PermissionRequest, 0, len(c.pending))
	for _, pending := range c.pending {
		if resolvedSessionID != "" && pending.request.SessionID != resolvedSessionID {
			continue
		}
		requests = append(requests, pending.request)
	}
	sort.Slice(requests, func(i, j int) bool { return requests[i].ID < requests[j].ID })
	return requests, nil
}

func (c *Agent) ReplyPermission(ctx context.Context, sessionID string, requestID string, reply types.PermissionReply) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("acp session id is required")
	}
	resolvedRequestID := strings.TrimSpace(requestID)
	if resolvedRequestID == "" {
		return fmt.Errorf("permission request id is required")
	}

	c.mu.Lock()
	pending, ok := c.pending[resolvedRequestID]
	if ok {
		delete(c.pending, resolvedRequestID)
	}
	c.mu.Unlock()
	if !ok {
		return types.ErrInteractionNoLongerPending
	}

	client, err := c.ensureConn(ctx)
	if err != nil {
		return err
	}
	optionID := pickPermissionOption(pending.options, reply)
	if optionID == "" {
		return client.respond(ctx, pending.rpcID, permissionResponse{
			Outcome: permissionOutcome{Outcome: "cancelled"},
		})
	}
	return client.respond(ctx, pending.rpcID, permissionResponse{
		Outcome: permissionOutcome{Outcome: "selected", OptionID: optionID},
	})
}

// pickPermissionOption maps an agent-bridge reply onto an option the agent
// offered, preferring exact kind matches and falling back to weaker ones.
func pickPermissionOption(options map[string]string, reply types.PermissionReply) string {
	wanted := []string{"allow_once"}
	switch reply {
	case types.PermissionReplyAlways:
		wanted = []string{"allow_always", "allow_once"}
	case types.PermissionReplyReject:
		wanted = []string{"reject_once", "reject_always"}
	}

	for _, kind := range wanted {
		best := ""
		for optionID, optionKind := range options {
			if optionKind != kind {
				continue
			}
			if best == "" || optionID < best {
				best = optionID
			}
		}
		if best != "" {
			return best
		}
	}
	return ""
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

func (c *Agent) closeSession(ctx context.Context, sessionID string) error {
	client, err := c.ensureConn(ctx)
	if err != nil {
		return err
	}
	if !c.capabilities().SessionCapabilities.Close.enabled() {
		return nil
	}
	return client.call(ctx, "session/close", closeSessionRequest{SessionID: sessionID}, nil)
}

// Connection lifecycle

func (c *Agent) callWithConn(ctx context.Context, method string, params, target any) error {
	client, err := c.ensureConn(ctx)
	if err != nil {
		return err
	}
	return client.call(ctx, method, params, target)
}

func (c *Agent) ensureConn(ctx context.Context) (*conn, error) {
	c.mu.Lock()
	if c.conn != nil {
		client := c.conn
		c.mu.Unlock()
		return client, nil
	}
	if c.initErr != nil {
		err := c.initErr
		c.mu.Unlock()
		return nil, err
	}
	c.mu.Unlock()

	c.startMu.Lock()
	defer c.startMu.Unlock()

	c.mu.Lock()
	if c.conn != nil {
		client := c.conn
		c.mu.Unlock()
		return client, nil
	}
	c.mu.Unlock()

	initCtx := ctx
	if c.initializeTimeout > 0 {
		var cancel context.CancelFunc
		initCtx, cancel = context.WithTimeout(ctx, c.initializeTimeout)
		defer cancel()
	}

	transport, err := c.factory(initCtx, c.directory)
	if err != nil {
		c.recordInitErr(err)
		return nil, err
	}
	client := newConn(transport, c.handleNotification, c.handleRequest)
	client.start()

	params := initializeRequest{
		ProtocolVersion: 1,
		ClientCapabilities: clientCapabilities{
			FS:       &fsCapabilities{ReadTextFile: false, WriteTextFile: false},
			Terminal: ptr(false),
		},
		ClientInfo: &implementation{Name: "agent-bridge", Title: "agent-bridge", Version: "0"},
	}
	var resp initializeResponse
	if err := client.call(initCtx, "initialize", params, &resp); err != nil {
		_ = client.close()
		c.recordInitErr(err)
		return nil, err
	}

	c.mu.Lock()
	c.conn = client
	c.caps = resp.AgentCapabilities
	c.initErr = nil
	c.mu.Unlock()
	return client, nil
}

func (c *Agent) capabilities() agentCapabilities {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.caps
}

func (c *Agent) recordInitErr(err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	c.mu.Lock()
	c.initErr = err
	c.mu.Unlock()
}

func (c *Agent) handleRequest(ctx context.Context, id json.RawMessage, method string, params json.RawMessage) {
	client, err := c.ensureConn(ctx)
	if err != nil {
		return
	}
	switch method {
	case "session/request_permission":
		var request permissionRequest
		if err := decodeRaw(params, &request); err != nil {
			_ = client.respondError(ctx, id, errCodeInternal, "invalid permission request: "+err.Error())
			return
		}
		options := make(map[string]string, len(request.Options))
		labels := make([]string, 0, len(request.Options))
		for _, option := range request.Options {
			options[strings.TrimSpace(option.OptionID)] = strings.TrimSpace(option.Kind)
			if name := strings.TrimSpace(option.Name); name != "" {
				labels = append(labels, name)
			}
		}
		c.mu.Lock()
		c.permSeq++
		requestID := fmt.Sprintf("perm-%d", c.permSeq)
		c.pending[requestID] = pendingPermission{
			request: types.PermissionRequest{
				ID:         requestID,
				SessionID:  strings.TrimSpace(request.SessionID),
				Permission: permissionTitle(request.ToolCall),
				Patterns:   labels,
				Metadata:   map[string]any{"tool_call": request.ToolCall},
				Tool: types.InteractionTool{
					MessageID: strings.TrimSpace(stringField(request.ToolCall, "messageId")),
					CallID:    strings.TrimSpace(stringField(request.ToolCall, "toolCallId")),
				},
			},
			rpcID:   id,
			options: options,
		}
		c.mu.Unlock()
	default:
		_ = client.respondError(ctx, id, errCodeMethodNotFound, "unsupported acp request: "+method)
	}
}

func (c *Agent) handleNotification(_ context.Context, method string, params json.RawMessage) {
	if method != "session/update" {
		return
	}
	var notification sessionUpdate
	if err := decodeRaw(params, &notification); err != nil {
		return
	}
	sessionID := strings.TrimSpace(notification.SessionID)
	if sessionID == "" {
		return
	}

	c.mu.Lock()
	state := c.ensureSessionLocked(sessionID)
	c.mu.Unlock()

	state.mu.Lock()
	turn := state.active
	switch update := notification.Update; update.SessionUpdate {
	case "config_option_update":
		state.options = update.ConfigOptions
		if choices := modelChoices(update.ConfigOptions); len(choices) > 0 {
			state.choices = choices
		}
		if model := modelFromOptions(update.ConfigOptions); !model.IsZero() {
			state.Model = model
		}
	case "session_info_update":
		if title := strings.TrimSpace(update.Title); title != "" {
			state.Title = title
		}
	}
	state.mu.Unlock()

	if turn == nil {
		return
	}
	update := notification.Update
	switch update.SessionUpdate {
	case "agent_message_chunk":
		if block := update.chunkContent(); block != nil {
			turn.appendAnswer(block.Text)
		}
	case "agent_thought_chunk":
		if block := update.chunkContent(); block != nil {
			turn.appendReasoning(block.Text)
		}
	case "tool_call":
		name := firstNonEmpty(strings.TrimSpace(update.Name), strings.TrimSpace(update.Title))
		input := rawJSONText(update.RawInput)
		state.mu.Lock()
		if state.helpers == nil {
			state.helpers = map[string]*toolCallState{}
		}
		state.helpers[strings.TrimSpace(update.ToolCallID)] = &toolCallState{Name: name, Input: input}
		state.mu.Unlock()
		turn.appendTool(renderToolCall(name, input, ""))
	case "tool_call_update":
		state.mu.Lock()
		helper := state.helpers[strings.TrimSpace(update.ToolCallID)]
		state.mu.Unlock()

		name := firstNonEmpty(strings.TrimSpace(update.Name), strings.TrimSpace(update.Title))
		if name == "" && helper != nil {
			name = helper.Name
		}
		output := firstNonEmpty(rawJSONText(update.RawOutput), renderToolContent(update.toolContentItems()))
		turn.appendTool(renderToolCall(name, "", output))
	case "plan":
		lines := make([]string, 0, len(update.Entries))
		for _, entry := range update.Entries {
			if content := strings.TrimSpace(entry.Content); content != "" {
				lines = append(lines, fmt.Sprintf("- [%s] %s", strings.TrimSpace(entry.Status), content))
			}
		}
		if len(lines) > 0 {
			turn.appendReasoning(strings.Join(lines, "\n"))
		}
	}
}

func modelFromOptions(options []configOption) types.ModelRef {
	for _, option := range options {
		if option.ID != "model" && option.Category != "model" {
			continue
		}
		return parseModelValue(option.CurrentValue)
	}
	return types.ModelRef{}
}

func (c *Agent) sessionOptions(sessionID string) []configOption {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.sessions[sessionID]
	if state == nil {
		return nil
	}
	return state.options
}

func (c *Agent) storeSession(session types.Session) {
	if strings.TrimSpace(session.ID) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.ensureSessionLocked(session.ID)
	if title := strings.TrimSpace(session.Title); title != "" {
		state.Title = title
	}
	if directory := strings.TrimSpace(session.Directory); directory != "" {
		state.Directory = directory
	}
}

func (c *Agent) ensureSessionLocked(sessionID string) *sessionState {
	state := c.sessions[sessionID]
	if state == nil {
		state = &sessionState{ID: sessionID, turns: []*turnState{}}
		c.sessions[sessionID] = state
	}
	return state
}

// Turn accumulation

func (t *turnState) appendAnswer(text string) {
	if text == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.answer.WriteString(text)
}

func (t *turnState) appendReasoning(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.reasoning.Len() > 0 {
		t.reasoning.WriteString("\n\n")
	}
	t.reasoning.WriteString(text)
}

func (t *turnState) appendTool(rendered string) {
	rendered = strings.TrimSpace(rendered)
	if rendered == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tools.Len() > 0 {
		t.tools.WriteString("\n")
	}
	t.tools.WriteString(rendered)
}

func (t *turnState) finish(stopReason string, err error) {
	t.mu.Lock()
	t.completedAt = float64(time.Now().UnixMilli()) / 1000
	t.stopReason = stopReason
	t.err = err
	if err != nil {
		if t.diagnostics.Len() > 0 {
			t.diagnostics.WriteString("\n")
		}
		t.diagnostics.WriteString(err.Error())
	}
	t.mu.Unlock()
	t.doneOnce.Do(func() { close(t.done) })
}

func (t *turnState) currentAnswer() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(t.answer.String())
}

func (t *turnState) currentCompletedAt() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.completedAt
}

func (t *turnState) snapshot() (answer, reasoning, tools, diagnostics string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(t.answer.String()),
		strings.TrimSpace(t.reasoning.String()),
		strings.TrimSpace(t.tools.String()),
		strings.TrimSpace(t.diagnostics.String())
}

// Helpers

func renderToolCall(name, input, output string) string {
	label := strings.TrimSpace(name)
	if label == "" {
		label = "tool"
	}
	builder := strings.Builder{}
	if resolvedInput := strings.TrimSpace(input); resolvedInput != "" {
		fmt.Fprintf(&builder, "<tool name=%q type=\"input\">\n%s\n</tool>", label, resolvedInput)
	}
	if resolvedOutput := strings.TrimSpace(output); resolvedOutput != "" {
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		fmt.Fprintf(&builder, "<tool name=%q type=\"output\">\n%s\n</tool>", label, resolvedOutput)
	}
	return builder.String()
}

func renderToolContent(items []toolCallContent) string {
	lines := make([]string, 0, len(items))
	for _, item := range items {
		switch item.Type {
		case "content":
			if item.Content == nil {
				continue
			}
			if text := strings.TrimSpace(item.Content.Text); text != "" {
				lines = append(lines, text)
			}
		case "diff":
			if path := strings.TrimSpace(item.Path); path != "" {
				lines = append(lines, path)
			}
		}
	}
	return strings.Join(lines, "\n")
}

func rawJSONText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case json.RawMessage:
		text := strings.TrimSpace(string(typed))
		if text == "null" {
			return ""
		}
		return text
	case string:
		return strings.TrimSpace(typed)
	default:
		b, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
}

func permissionTitle(toolCall map[string]any) string {
	title := firstNonEmpty(
		stringField(toolCall, "title"),
		stringField(toolCall, "name"),
		stringField(toolCall, "toolName"),
		stringField(toolCall, "kind"),
	)
	if title == "" {
		return "Permission required"
	}
	return title
}

func stringField(values map[string]any, key string) string {
	value, ok := values[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return text
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func ptr[T any](value T) *T {
	return &value
}
