package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gitsang/agent-bridge/internal/agent"
	"github.com/gitsang/agent-bridge/internal/types"
)

const CodexProviderID = "codex"

// Transport is the newline-delimited JSON-RPC channel used by Codex app-server.
type Transport interface {
	ReadMessage(ctx context.Context) (json.RawMessage, error)
	WriteMessage(ctx context.Context, msg json.RawMessage) error
	Close() error
}

type TransportFactory func(ctx context.Context) (Transport, error)

type Option func(*Options)

type Options struct {
	Logger            *slog.Logger
	Command           string
	Args              []string
	Env               map[string]string
	Timeout           time.Duration
	InitializeTimeout time.Duration
	TransportFactory  TransportFactory
}

type Client struct {
	factory           TransportFactory
	timeout           time.Duration
	initializeTimeout time.Duration

	mu          sync.Mutex
	startMu     sync.Mutex
	conn        *rpcConn
	sessions    map[string]*sessionState
	pendingPerm map[string]pendingPermission
	pendingQues map[string]pendingQuestion
}

type sessionState struct {
	ID        string
	Title     string
	Directory string
	Model     types.ModelRef
	Turns     map[string]*turnState
}

type turnState struct {
	ID          string
	SessionID   string
	Model       types.ModelRef
	CompletedAt float64
	Status      string
	Answer      strings.Builder
	Reasoning   strings.Builder
	Action      strings.Builder
	Artifact    strings.Builder
	Diagnostic  strings.Builder
	Done        chan struct{}
	Err         error
	doneOnce    sync.Once
}

type pendingPermission struct {
	request types.PermissionRequest
	rpcID   json.RawMessage
	kind    string
}

type pendingQuestion struct {
	request     types.QuestionRequest
	rpcID       json.RawMessage
	questionIDs []string
}

type jsonRPCMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
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

func NewClient(options ...Option) *Client {
	resolved := Options{
		Logger:            slog.Default(),
		Command:           "codex",
		Args:              []string{"app-server", "--listen", "stdio://"},
		Timeout:           30 * time.Minute,
		InitializeTimeout: 15 * time.Second,
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
	factory := resolved.TransportFactory
	if factory == nil {
		command := strings.TrimSpace(resolved.Command)
		args := append([]string(nil), resolved.Args...)
		env := copyStringMap(resolved.Env)
		factory = func(ctx context.Context) (Transport, error) {
			return newProcessTransport(ctx, command, args, env)
		}
	}

	return &Client{
		factory:           factory,
		timeout:           resolved.Timeout,
		initializeTimeout: resolved.InitializeTimeout,
		sessions:          map[string]*sessionState{},
		pendingPerm:       map[string]pendingPermission{},
		pendingQues:       map[string]pendingQuestion{},
	}
}

func (c *Client) ListModels(ctx context.Context, _ string) ([]types.ModelInfo, error) {
	var resp struct {
		Data []struct {
			ID          string `json:"id"`
			Model       string `json:"model"`
			DisplayName string `json:"displayName"`
			Hidden      bool   `json:"hidden"`
		} `json:"data"`
	}
	if err := c.call(ctx, "model/list", map[string]any{"includeHidden": true}, &resp); err != nil {
		return nil, err
	}
	models := make([]types.ModelInfo, 0, len(resp.Data))
	for _, item := range resp.Data {
		modelID := firstNonEmpty(item.Model, item.ID)
		if modelID == "" {
			continue
		}
		models = append(models, types.ModelInfo{
			ModelRef:     types.ModelRef{ProviderID: CodexProviderID, ModelID: modelID},
			ProviderName: "Codex",
			ModelName:    firstNonEmpty(item.DisplayName, modelID),
		})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ModelID < models[j].ModelID })
	return models, nil
}

func (c *Client) ResolveModel(ctx context.Context, spec, directory string) (types.ModelRef, error) {
	resolvedModel := strings.TrimSpace(spec)
	if resolvedModel == "" {
		return types.ModelRef{}, fmt.Errorf("model is required")
	}
	if strings.Contains(resolvedModel, "/") {
		pair := strings.SplitN(resolvedModel, "/", 2)
		providerID := strings.TrimSpace(pair[0])
		modelID := strings.TrimSpace(pair[1])
		if modelID == "" {
			return types.ModelRef{}, fmt.Errorf("invalid model format: %s", resolvedModel)
		}
		if providerID != "" && !strings.EqualFold(providerID, CodexProviderID) {
			return types.ModelRef{}, fmt.Errorf("unsupported codex model provider: %s", providerID)
		}
		return types.ModelRef{ProviderID: CodexProviderID, ModelID: modelID}, nil
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

func (c *Client) ListAgents(context.Context, string) ([]types.AgentInfo, error) {
	return []types.AgentInfo{{Name: "codex", Description: "Codex coding agent", Mode: "default"}}, nil
}

func (c *Client) ListSessions(ctx context.Context, directory string) ([]types.Session, error) {
	params := map[string]any{"limit": 100, "sourceKinds": []string{}}
	if strings.TrimSpace(directory) != "" {
		params["cwd"] = strings.TrimSpace(directory)
	}
	var resp struct {
		Data []codexThread `json:"data"`
	}
	if err := c.call(ctx, "thread/list", params, &resp); err != nil {
		return nil, err
	}
	sessions := make([]types.Session, 0, len(resp.Data))
	for _, thread := range resp.Data {
		if strings.TrimSpace(thread.ID) == "" {
			continue
		}
		session := toSession(thread)
		sessions = append(sessions, session)
		c.storeSession(session)
	}
	return sessions, nil
}

func (c *Client) ListAllSessions(ctx context.Context) ([]types.Session, error) {
	return c.ListSessions(ctx, "")
}

func (c *Client) GetSession(ctx context.Context, sessionID string) (*types.Session, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("codex session id is required")
	}

	c.mu.Lock()
	if state := c.sessions[resolvedSessionID]; state != nil {
		session := types.Session{ID: state.ID, Title: state.Title, Directory: state.Directory}
		c.mu.Unlock()
		return &session, nil
	}
	c.mu.Unlock()

	var resp struct {
		Thread codexThread `json:"thread"`
	}
	if err := c.call(ctx, "thread/read", map[string]any{"threadId": resolvedSessionID, "includeTurns": false}, &resp); err != nil {
		return nil, err
	}
	session := toSession(resp.Thread)
	if strings.TrimSpace(session.ID) == "" {
		return nil, nil
	}
	c.storeSession(session)
	return &session, nil
}

func (c *Client) CreateSession(ctx context.Context, request types.CreateSessionRequest) (*types.Session, error) {
	params := map[string]any{"ephemeral": false}
	if directory := strings.TrimSpace(request.Directory); directory != "" {
		params["cwd"] = directory
	}
	var resp struct {
		Thread codexThread `json:"thread"`
	}
	if err := c.call(ctx, "thread/start", params, &resp); err != nil {
		return nil, err
	}
	session := toSession(resp.Thread)
	if strings.TrimSpace(session.ID) == "" {
		return nil, fmt.Errorf("created codex thread id is required")
	}
	if title := strings.TrimSpace(request.Title); title != "" {
		if err := c.call(ctx, "thread/name/set", map[string]any{"threadId": session.ID, "name": title}, nil); err != nil {
			return nil, err
		}
		session.Title = title
	}
	if session.Directory == "" {
		session.Directory = strings.TrimSpace(request.Directory)
	}
	c.storeSession(session)
	return &session, nil
}

func (c *Client) GetMessages(ctx context.Context, sessionID string) ([]types.Message, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("codex session id is required")
	}
	var resp struct {
		Thread codexThread `json:"thread"`
	}
	if err := c.call(ctx, "thread/read", map[string]any{"threadId": resolvedSessionID, "includeTurns": true}, &resp); err != nil {
		return nil, err
	}
	messages := make([]types.Message, 0)
	for _, turn := range resp.Thread.Turns {
		for _, item := range turn.Items {
			itemID, role, content := itemRoleAndContent(item)
			if role == "" {
				continue
			}
			messages = append(messages, types.Message{
				ID:          firstNonEmpty(itemID, turn.ID),
				SessionID:   resolvedSessionID,
				Role:        role,
				Content:     content,
				CompletedAt: firstPositive(turn.CompletedAt, turn.StartedAt),
				Model:       types.ModelRef{ProviderID: CodexProviderID, ModelID: firstNonEmpty(resp.Thread.Model, resp.Thread.ModelProvider)},
			})
		}
	}
	return messages, nil
}

func (c *Client) GetLatestAssistantMessage(ctx context.Context, sessionID string) (*types.Message, error) {
	c.mu.Lock()
	var latest *types.Message
	if state := c.sessions[strings.TrimSpace(sessionID)]; state != nil {
		for _, turn := range state.Turns {
			if turn.CompletedAt <= 0 || turn.Answer.Len() == 0 {
				continue
			}
			if latest == nil || turn.CompletedAt > latest.CompletedAt {
				latest = &types.Message{
					ID:          turn.ID,
					SessionID:   turn.SessionID,
					Role:        "assistant",
					Content:     strings.TrimSpace(turn.Answer.String()),
					Reasoning:   strings.TrimSpace(turn.Reasoning.String()),
					Tools:       strings.TrimSpace(turn.Action.String()),
					Patches:     strings.TrimSpace(turn.Artifact.String()),
					Diagnostics: strings.TrimSpace(turn.Diagnostic.String()),
					CompletedAt: turn.CompletedAt,
					Model:       turn.Model,
				}
			}
		}
	}
	c.mu.Unlock()
	if latest != nil {
		return latest, nil
	}

	messages, err := c.GetMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			msg := messages[i]
			return &msg, nil
		}
	}
	return nil, nil
}

func (c *Client) Prompt(ctx context.Context, sessionID string, prompt string, optfs ...types.PromptOptionFunc) (*types.PromptHandle, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("codex session id is required")
	}
	resolvedContent := strings.TrimSpace(prompt)
	if resolvedContent == "" {
		return nil, fmt.Errorf("message content is required")
	}
	resolvedOptions := types.PromptOptions{}
	for _, apply := range optfs {
		if apply != nil {
			apply(&resolvedOptions)
		}
	}

	doneCh := make(chan struct{})
	errCh := make(chan error, 1)
	c.runPrompt(ctx, resolvedSessionID, resolvedContent, resolvedOptions, doneCh, errCh)
	return types.NewPromptHandle(doneCh, errCh), nil
}

func (c *Client) runPrompt(ctx context.Context, sessionID, content string, options types.PromptOptions, doneCh chan struct{}, errCh chan error) {
	promptCtx := ctx
	var cancel context.CancelFunc
	if c.timeout > 0 {
		promptCtx, cancel = context.WithTimeout(ctx, c.timeout)
	}

	cancelPrompt := func() {
		if cancel != nil {
			cancel()
		}
	}

	params := map[string]any{
		"threadId": sessionID,
		"input": []map[string]any{{
			"type":          "text",
			"text":          content,
			"text_elements": []any{},
		}},
	}
	if directory := strings.TrimSpace(options.Directory); directory != "" {
		params["cwd"] = directory
		c.updateSessionDirectory(sessionID, directory)
	}
	if !options.Model.IsZero() {
		params["model"] = options.Model.ModelID
		c.updateSessionModel(sessionID, options.Model)
	}

	var resp struct {
		Turn codexTurn `json:"turn"`
	}
	if err := c.call(promptCtx, "turn/start", params, &resp); err != nil {
		cancelPrompt()
		errCh <- err
		return
	}
	turnID := strings.TrimSpace(resp.Turn.ID)
	if turnID == "" {
		cancelPrompt()
		errCh <- fmt.Errorf("codex turn id is required")
		return
	}

	turn := c.ensureTurn(sessionID, turnID, options.Model)
	go func() {
		defer cancelPrompt()
		select {
		case <-turn.Done:
			if turn.Err != nil {
				errCh <- turn.Err
				return
			}
			close(doneCh)
		case <-promptCtx.Done():
			_ = c.interruptTurn(context.Background(), sessionID, turnID)
			c.mu.Lock()
			turn.Status = "interrupted"
			turn.CompletedAt = nowCompletedAt()
			turn.Err = promptCtx.Err()
			c.mu.Unlock()
			turn.doneOnce.Do(func() { close(turn.Done) })
			errCh <- promptCtx.Err()
		}
	}()
}

func (c *Client) PollMessagesAfter(_ context.Context, sessionID string, afterCompletedAt float64, output types.MessageOutputOptions) ([]*types.Message, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("codex session id is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.sessions[resolvedSessionID]
	if state == nil {
		return nil, nil
	}
	turns := make([]*turnState, 0, len(state.Turns))
	for _, turn := range state.Turns {
		if turn.CompletedAt > afterCompletedAt {
			turns = append(turns, turn)
		}
	}
	sort.Slice(turns, func(i, j int) bool { return turns[i].CompletedAt < turns[j].CompletedAt })

	messages := make([]*types.Message, 0, len(turns))
	for _, turn := range turns {
		content := turn.extractContent(output)
		if content.Answer == "" && content.Reasoning == "" && content.Tools == "" && content.Patches == "" && content.Diagnostics == "" {
			continue
		}
		messages = append(messages, &types.Message{
			ID:          turn.ID,
			SessionID:   turn.SessionID,
			Role:        "assistant",
			Content:     content.Answer,
			Reasoning:   content.Reasoning,
			Tools:       content.Tools,
			Patches:     content.Patches,
			Diagnostics: content.Diagnostics,
			CompletedAt: turn.CompletedAt,
			Model:       turn.Model,
		})
	}
	return messages, nil
}

func (c *Client) ListPendingPermissions(_ context.Context, sessionID string) ([]types.PermissionRequest, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	c.mu.Lock()
	defer c.mu.Unlock()
	requests := make([]types.PermissionRequest, 0, len(c.pendingPerm))
	for _, pending := range c.pendingPerm {
		if resolvedSessionID != "" && pending.request.SessionID != resolvedSessionID {
			continue
		}
		requests = append(requests, pending.request)
	}
	sort.Slice(requests, func(i, j int) bool { return requests[i].ID < requests[j].ID })
	return requests, nil
}

func (c *Client) ReplyPermission(ctx context.Context, sessionID string, requestID string, reply types.PermissionReply) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("codex session id is required")
	}
	resolvedRequestID := strings.TrimSpace(requestID)
	if resolvedRequestID == "" {
		return fmt.Errorf("permission request id is required")
	}
	resolvedReply := types.PermissionReply(strings.TrimSpace(string(reply)))
	if resolvedReply != types.PermissionReplyOnce && resolvedReply != types.PermissionReplyAlways && resolvedReply != types.PermissionReplyReject {
		return fmt.Errorf("unsupported permission reply: %s", resolvedReply)
	}

	c.mu.Lock()
	pending, ok := c.pendingPerm[resolvedRequestID]
	if ok {
		delete(c.pendingPerm, resolvedRequestID)
	}
	c.mu.Unlock()
	if !ok {
		return types.ErrInteractionNoLongerPending
	}

	decision := "decline"
	if resolvedReply == types.PermissionReplyOnce {
		decision = "accept"
	}
	if resolvedReply == types.PermissionReplyAlways {
		decision = "acceptForSession"
	}
	result := map[string]any{"decision": decision}
	if pending.kind == "permissions" {
		result = map[string]any{"permissions": map[string]any{}, "scope": mapPermissionScope(resolvedReply)}
	}
	return c.respond(ctx, pending.rpcID, result)
}

func (c *Client) ListPendingQuestions(_ context.Context, sessionID string) ([]types.QuestionRequest, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	c.mu.Lock()
	defer c.mu.Unlock()
	requests := make([]types.QuestionRequest, 0, len(c.pendingQues))
	for _, pending := range c.pendingQues {
		if resolvedSessionID != "" && pending.request.SessionID != resolvedSessionID {
			continue
		}
		requests = append(requests, pending.request)
	}
	sort.Slice(requests, func(i, j int) bool { return requests[i].ID < requests[j].ID })
	return requests, nil
}

func (c *Client) ReplyQuestion(ctx context.Context, sessionID string, requestID string, answers [][]string) error {
	return c.replyQuestion(ctx, sessionID, requestID, answers, false)
}

func (c *Client) RejectQuestion(ctx context.Context, sessionID string, requestID string) error {
	return c.replyQuestion(ctx, sessionID, requestID, nil, true)
}

func (c *Client) replyQuestion(ctx context.Context, sessionID string, requestID string, answers [][]string, reject bool) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("codex session id is required")
	}
	resolvedRequestID := strings.TrimSpace(requestID)
	if resolvedRequestID == "" {
		return fmt.Errorf("question request id is required")
	}
	c.mu.Lock()
	pending, ok := c.pendingQues[resolvedRequestID]
	if ok {
		delete(c.pendingQues, resolvedRequestID)
	}
	c.mu.Unlock()
	if !ok {
		return types.ErrInteractionNoLongerPending
	}
	if reject {
		return c.respond(ctx, pending.rpcID, map[string]any{"answers": map[string]any{}})
	}
	if len(answers) == 0 {
		return fmt.Errorf("question answers are required")
	}
	resultAnswers := map[string]any{}
	for i, questionID := range pending.questionIDs {
		answerSet := []string{}
		if i < len(answers) {
			answerSet = trimStringSlice(answers[i])
		}
		if len(answerSet) == 0 {
			return fmt.Errorf("question answers are required")
		}
		resultAnswers[questionID] = map[string]any{"answers": answerSet}
	}
	return c.respond(ctx, pending.rpcID, map[string]any{"answers": resultAnswers})
}

func (c *Client) call(ctx context.Context, method string, params any, target any) error {
	conn, err := c.ensureConn(ctx)
	if err != nil {
		return err
	}
	return conn.call(ctx, method, params, target)
}

func (c *Client) respond(ctx context.Context, id json.RawMessage, result any) error {
	conn, err := c.ensureConn(ctx)
	if err != nil {
		return err
	}
	return conn.respond(ctx, id, result)
}

func (c *Client) ensureConn(ctx context.Context) (*rpcConn, error) {
	c.mu.Lock()
	if c.conn != nil {
		conn := c.conn
		c.mu.Unlock()
		return conn, nil
	}
	c.mu.Unlock()

	c.startMu.Lock()
	defer c.startMu.Unlock()

	c.mu.Lock()
	if c.conn != nil {
		conn := c.conn
		c.mu.Unlock()
		return conn, nil
	}
	c.mu.Unlock()

	initCtx := ctx
	var cancel context.CancelFunc
	if c.initializeTimeout > 0 {
		initCtx, cancel = context.WithTimeout(ctx, c.initializeTimeout)
	}
	if cancel != nil {
		defer cancel()
	}

	transport, err := c.factory(initCtx)
	if err != nil {
		return nil, err
	}
	conn := newRPCConn(transport, c.handleIncoming)
	conn.start()
	params := map[string]any{
		"clientInfo":   map[string]any{"name": "agent-bridge", "title": "agent-bridge", "version": "0"},
		"capabilities": map[string]any{"experimentalApi": true, "requestAttestation": false},
	}
	var ignored map[string]any
	if err := conn.call(initCtx, "initialize", params, &ignored); err != nil {
		_ = conn.close()
		return nil, err
	}

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	return conn, nil
}

func (c *Client) handleIncoming(ctx context.Context, msg jsonRPCMessage) {
	if msg.Method == "" {
		return
	}
	if msg.ID != nil {
		c.handleServerRequest(ctx, msg)
		return
	}
	switch msg.Method {
	case "thread/started":
		var params struct {
			Thread codexThread `json:"thread"`
		}
		if decodeRaw(msg.Params, &params) == nil {
			c.storeSession(toSession(params.Thread))
		}
	case "item/agentMessage/delta":
		var params struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Delta    string `json:"delta"`
		}
		if decodeRaw(msg.Params, &params) == nil {
			c.appendTurnText(params.ThreadID, params.TurnID, types.MessageContentAnswer, params.Delta)
		}
	case "item/reasoning/textDelta", "item/reasoning/summaryTextDelta":
		var params struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Delta    string `json:"delta"`
		}
		if decodeRaw(msg.Params, &params) == nil {
			c.appendTurnText(params.ThreadID, params.TurnID, types.MessageContentReasoning, params.Delta)
		}
	case "item/commandExecution/outputDelta":
		var params struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Delta    string `json:"delta"`
		}
		if decodeRaw(msg.Params, &params) == nil {
			c.appendTurnText(params.ThreadID, params.TurnID, types.MessageContentActionTool, params.Delta)
		}
	case "turn/diff/updated":
		var params struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Diff     string `json:"diff"`
		}
		if decodeRaw(msg.Params, &params) == nil {
			c.appendTurnText(params.ThreadID, params.TurnID, types.MessageContentArtifactPatch, params.Diff)
		}
	case "item/fileChange/outputDelta", "item/fileChange/patchUpdated":
		var params struct {
			ThreadID string          `json:"threadId"`
			TurnID   string          `json:"turnId"`
			Delta    string          `json:"delta"`
			Changes  json.RawMessage `json:"changes"`
		}
		if decodeRaw(msg.Params, &params) == nil {
			text := params.Delta
			if text == "" && len(params.Changes) > 0 {
				text = string(params.Changes)
			}
			c.appendTurnText(params.ThreadID, params.TurnID, types.MessageContentArtifactPatch, text)
		}
	case "error":
		var params struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Error    struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if decodeRaw(msg.Params, &params) == nil {
			c.completeTurn(params.ThreadID, params.TurnID, "failed", nowCompletedAt(), errors.New(firstNonEmpty(params.Error.Message, "codex turn failed")))
		}
	case "turn/completed":
		var params struct {
			ThreadID string    `json:"threadId"`
			Turn     codexTurn `json:"turn"`
		}
		if decodeRaw(msg.Params, &params) == nil {
			completedAt := firstPositive(params.Turn.CompletedAt, nowCompletedAt())
			var err error
			if params.Turn.Status == "failed" && params.Turn.Error != nil {
				err = errors.New(firstNonEmpty(params.Turn.Error.Message, "codex turn failed"))
			}
			c.completeTurn(params.ThreadID, params.Turn.ID, params.Turn.Status, completedAt, err)
		}
	}
}

func (c *Client) handleServerRequest(ctx context.Context, msg jsonRPCMessage) {
	switch msg.Method {
	case "item/commandExecution/requestApproval":
		var params commandApprovalParams
		if decodeRaw(msg.Params, &params) != nil {
			_ = c.respond(ctx, msg.ID, map[string]any{"decision": "decline"})
			return
		}
		requestID := stringRawID(msg.ID)
		request := types.PermissionRequest{ID: requestID, SessionID: strings.TrimSpace(params.ThreadID), Permission: formatCommandPermission(params), Patterns: trimStringSlice([]string{params.CWD}), Metadata: map[string]any{"turn_id": params.TurnID, "item_id": params.ItemID, "reason": params.Reason}}
		c.mu.Lock()
		c.pendingPerm[requestID] = pendingPermission{request: request, rpcID: msg.ID, kind: "command"}
		c.mu.Unlock()
	case "item/fileChange/requestApproval":
		var params fileApprovalParams
		if decodeRaw(msg.Params, &params) != nil {
			_ = c.respond(ctx, msg.ID, map[string]any{"decision": "decline"})
			return
		}
		requestID := stringRawID(msg.ID)
		request := types.PermissionRequest{ID: requestID, SessionID: strings.TrimSpace(params.ThreadID), Permission: formatFilePermission(params), Patterns: trimStringSlice([]string{params.GrantRoot}), Metadata: map[string]any{"turn_id": params.TurnID, "item_id": params.ItemID, "reason": params.Reason}}
		c.mu.Lock()
		c.pendingPerm[requestID] = pendingPermission{request: request, rpcID: msg.ID, kind: "file"}
		c.mu.Unlock()
	case "item/permissions/requestApproval":
		var params permissionProfileParams
		if decodeRaw(msg.Params, &params) != nil {
			_ = c.respond(ctx, msg.ID, map[string]any{"permissions": map[string]any{}, "scope": "turn"})
			return
		}
		requestID := stringRawID(msg.ID)
		request := types.PermissionRequest{ID: requestID, SessionID: strings.TrimSpace(params.ThreadID), Permission: firstNonEmpty(params.Reason, "Codex requests additional permissions"), Patterns: trimStringSlice([]string{params.CWD}), Metadata: map[string]any{"turn_id": params.TurnID, "item_id": params.ItemID, "reason": params.Reason}}
		c.mu.Lock()
		c.pendingPerm[requestID] = pendingPermission{request: request, rpcID: msg.ID, kind: "permissions"}
		c.mu.Unlock()
	case "item/tool/requestUserInput":
		var params toolInputParams
		if decodeRaw(msg.Params, &params) != nil {
			_ = c.respond(ctx, msg.ID, map[string]any{"answers": map[string]any{}})
			return
		}
		requestID := stringRawID(msg.ID)
		questions := make([]types.Question, 0, len(params.Questions))
		questionIDs := make([]string, 0, len(params.Questions))
		for _, question := range params.Questions {
			questionID := strings.TrimSpace(question.ID)
			if questionID == "" {
				questionID = fmt.Sprintf("q%d", len(questionIDs)+1)
			}
			questionIDs = append(questionIDs, questionID)
			options := make([]string, 0, len(question.Options))
			for _, option := range question.Options {
				if label := strings.TrimSpace(option.Label); label != "" {
					options = append(options, label)
				}
			}
			questions = append(questions, types.Question{Text: firstNonEmpty(question.Question, question.Header), Options: options, Multiple: false})
		}
		request := types.QuestionRequest{ID: requestID, SessionID: strings.TrimSpace(params.ThreadID), Questions: questions, Tool: types.InteractionTool{CallID: strings.TrimSpace(params.ItemID)}}
		c.mu.Lock()
		c.pendingQues[requestID] = pendingQuestion{request: request, rpcID: msg.ID, questionIDs: questionIDs}
		c.mu.Unlock()
	default:
		_ = c.respondError(ctx, msg.ID, -32601, fmt.Sprintf("unsupported codex app-server request: %s", msg.Method))
	}
}

func (c *Client) respondError(ctx context.Context, id json.RawMessage, code int64, message string) error {
	conn, err := c.ensureConn(ctx)
	if err != nil {
		return err
	}
	return conn.respondError(ctx, id, code, message)
}

func (c *Client) storeSession(session types.Session) {
	if strings.TrimSpace(session.ID) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.sessions[session.ID]
	if state == nil {
		state = &sessionState{ID: session.ID, Turns: map[string]*turnState{}}
		c.sessions[session.ID] = state
	}
	if strings.TrimSpace(session.Title) != "" {
		state.Title = strings.TrimSpace(session.Title)
	}
	if strings.TrimSpace(session.Directory) != "" {
		state.Directory = strings.TrimSpace(session.Directory)
	}
}

func (c *Client) updateSessionDirectory(sessionID, directory string) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(directory) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.ensureSessionLocked(sessionID)
	state.Directory = strings.TrimSpace(directory)
}

func (c *Client) updateSessionModel(sessionID string, model types.ModelRef) {
	if strings.TrimSpace(sessionID) == "" || model.IsZero() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.ensureSessionLocked(sessionID)
	state.Model = model
}

func (c *Client) ensureTurn(sessionID, turnID string, model types.ModelRef) *turnState {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.ensureSessionLocked(sessionID)
	turn := state.Turns[turnID]
	if turn == nil {
		turn = &turnState{ID: turnID, SessionID: sessionID, Done: make(chan struct{})}
		state.Turns[turnID] = turn
	}
	if !model.IsZero() {
		turn.Model = model
	} else if !state.Model.IsZero() {
		turn.Model = state.Model
	} else {
		turn.Model = types.ModelRef{ProviderID: CodexProviderID, ModelID: ""}
	}
	return turn
}

func (c *Client) ensureSessionLocked(sessionID string) *sessionState {
	state := c.sessions[sessionID]
	if state == nil {
		state = &sessionState{ID: sessionID, Turns: map[string]*turnState{}}
		c.sessions[sessionID] = state
	}
	return state
}

func (c *Client) appendTurnText(sessionID, turnID string, kind types.MessageContentKind, text string) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(turnID) == "" || text == "" {
		return
	}
	turn := c.ensureTurn(sessionID, turnID, types.ModelRef{})
	c.mu.Lock()
	defer c.mu.Unlock()
	switch kind {
	case types.MessageContentAnswer:
		turn.Answer.WriteString(text)
	case types.MessageContentReasoning:
		turn.Reasoning.WriteString(text)
	case types.MessageContentActionTool:
		turn.Action.WriteString(text)
	case types.MessageContentArtifactPatch:
		turn.Artifact.WriteString(text)
	default:
		turn.Diagnostic.WriteString(text)
	}
}

func (c *Client) completeTurn(sessionID, turnID, status string, completedAt float64, err error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(turnID) == "" {
		return
	}
	turn := c.ensureTurn(sessionID, turnID, types.ModelRef{})
	c.mu.Lock()
	turn.Status = status
	turn.CompletedAt = completedAt
	turn.Err = err
	c.mu.Unlock()
	turn.doneOnce.Do(func() { close(turn.Done) })
}

func (c *Client) interruptTurn(ctx context.Context, sessionID, turnID string) error {
	return c.call(ctx, "turn/interrupt", map[string]any{"threadId": sessionID, "turnId": turnID}, nil)
}

func (t *turnState) render(output types.MessageOutputOptions) string {
	builder := strings.Builder{}
	if output.Includes(types.MessageContentReasoning) && t.Reasoning.Len() > 0 {
		fmt.Fprintf(&builder, "\n<thinking>\n%s\n</thinking>", strings.TrimSpace(t.Reasoning.String()))
	}
	if output.Includes(types.MessageContentActionTool) && t.Action.Len() > 0 {
		fmt.Fprintf(&builder, "\n<tool type=\"output\">\n%s\n</tool>", strings.TrimSpace(t.Action.String()))
	}
	if output.Includes(types.MessageContentArtifactPatch) && t.Artifact.Len() > 0 {
		fmt.Fprintf(&builder, "\n<patch>\n%s\n</patch>", strings.TrimSpace(t.Artifact.String()))
	}
	if output.Includes(types.MessageContentDiagnostic) && t.Diagnostic.Len() > 0 {
		fmt.Fprintf(&builder, "\n<diagnostic>\n%s\n</diagnostic>", strings.TrimSpace(t.Diagnostic.String()))
	}
	if output.Includes(types.MessageContentAnswer) && t.Answer.Len() > 0 {
		builder.WriteString("\n" + strings.TrimSpace(t.Answer.String()))
	}
	return strings.TrimSpace(builder.String())
}

func (t *turnState) extractContent(output types.MessageOutputOptions) extractedContent {
	var result extractedContent
	if output.Includes(types.MessageContentAnswer) && t.Answer.Len() > 0 {
		result.Answer = strings.TrimSpace(t.Answer.String())
	}
	if output.Includes(types.MessageContentReasoning) && t.Reasoning.Len() > 0 {
		result.Reasoning = strings.TrimSpace(t.Reasoning.String())
	}
	if output.Includes(types.MessageContentActionTool) && t.Action.Len() > 0 {
		result.Tools = strings.TrimSpace(t.Action.String())
	}
	if output.Includes(types.MessageContentArtifactPatch) && t.Artifact.Len() > 0 {
		result.Patches = strings.TrimSpace(t.Artifact.String())
	}
	if output.Includes(types.MessageContentDiagnostic) && t.Diagnostic.Len() > 0 {
		result.Diagnostics = strings.TrimSpace(t.Diagnostic.String())
	}
	return result
}

type extractedContent struct {
	Answer      string
	Reasoning   string
	Tools       string
	Patches     string
	Diagnostics string
}

// JSON-RPC connection

type rpcConn struct {
	transport Transport
	onMessage func(context.Context, jsonRPCMessage)
	nextID    atomic.Int64
	pendingMu sync.Mutex
	pending   map[string]chan jsonRPCMessage
	closed    chan struct{}
}

func newRPCConn(transport Transport, onMessage func(context.Context, jsonRPCMessage)) *rpcConn {
	return &rpcConn{transport: transport, onMessage: onMessage, pending: map[string]chan jsonRPCMessage{}, closed: make(chan struct{})}
}

func (c *rpcConn) start() {
	go func() {
		defer close(c.closed)
		for {
			raw, err := c.transport.ReadMessage(context.Background())
			if err != nil {
				c.failPending(err)
				return
			}
			var msg jsonRPCMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			if msg.Method == "" && msg.ID != nil {
				key := string(msg.ID)
				c.pendingMu.Lock()
				ch := c.pending[key]
				delete(c.pending, key)
				c.pendingMu.Unlock()
				if ch != nil {
					ch <- msg
					close(ch)
				}
				continue
			}
			if c.onMessage != nil {
				c.onMessage(context.Background(), msg)
			}
		}
	}()
}

func (c *rpcConn) call(ctx context.Context, method string, params any, target any) error {
	id := c.nextID.Add(1)
	idRaw, _ := json.Marshal(id)
	paramsRaw := mustMarshalRaw(params)
	payload, err := json.Marshal(jsonRPCMessage{ID: idRaw, Method: method, Params: paramsRaw})
	if err != nil {
		return err
	}
	ch := make(chan jsonRPCMessage, 1)
	c.pendingMu.Lock()
	c.pending[string(idRaw)] = ch
	c.pendingMu.Unlock()
	if err := c.transport.WriteMessage(ctx, payload); err != nil {
		c.pendingMu.Lock()
		delete(c.pending, string(idRaw))
		c.pendingMu.Unlock()
		return err
	}
	select {
	case msg := <-ch:
		if msg.Error != nil {
			return fmt.Errorf("codex JSON-RPC error %d: %s", msg.Error.Code, msg.Error.Message)
		}
		if target != nil && len(msg.Result) > 0 {
			if err := json.Unmarshal(msg.Result, target); err != nil {
				return err
			}
		}
		return nil
	case <-ctx.Done():
		c.pendingMu.Lock()
		delete(c.pending, string(idRaw))
		c.pendingMu.Unlock()
		return ctx.Err()
	case <-c.closed:
		return io.EOF
	}
}

func (c *rpcConn) respond(ctx context.Context, id json.RawMessage, result any) error {
	payload, err := json.Marshal(jsonRPCMessage{ID: id, Result: mustMarshalRaw(result)})
	if err != nil {
		return err
	}
	return c.transport.WriteMessage(ctx, payload)
}

func (c *rpcConn) respondError(ctx context.Context, id json.RawMessage, code int64, message string) error {
	payload, err := json.Marshal(jsonRPCMessage{ID: id, Error: &jsonRPCError{Code: code, Message: message}})
	if err != nil {
		return err
	}
	return c.transport.WriteMessage(ctx, payload)
}

func (c *rpcConn) close() error {
	return c.transport.Close()
}

func (c *rpcConn) failPending(err error) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for key, ch := range c.pending {
		delete(c.pending, key)
		ch <- jsonRPCMessage{Error: &jsonRPCError{Code: -32000, Message: err.Error()}}
		close(ch)
	}
}

// process transport

type processTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	mu     sync.Mutex
}

func newProcessTransport(ctx context.Context, command string, args []string, env map[string]string) (*processTransport, error) {
	resolvedCommand := firstNonEmpty(command, "codex")
	cmd := exec.CommandContext(ctx, resolvedCommand, args...)
	if len(args) == 0 {
		cmd.Args = []string{resolvedCommand, "app-server", "--listen", "stdio://"}
	}
	if len(env) > 0 {
		cmd.Env = os.Environ()
		for key, value := range env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", key, value))
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
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
	go io.Copy(io.Discard, stderr)
	return &processTransport{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}, nil
}

func (t *processTransport) ReadMessage(ctx context.Context) (json.RawMessage, error) {
	type result struct {
		data json.RawMessage
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := t.stdout.ReadBytes('\n')
		if err != nil {
			ch <- result{err: err}
			return
		}
		ch <- result{data: json.RawMessage(strings.TrimSpace(string(line)))}
	}()
	select {
	case res := <-ch:
		return res.data, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *processTransport) WriteMessage(ctx context.Context, msg json.RawMessage) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, err := t.stdin.Write(append(msg, '\n'))
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func (t *processTransport) Close() error {
	_ = t.stdin.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	return t.cmd.Wait()
}

// Codex protocol shapes

type codexThread struct {
	ID            string      `json:"id"`
	Preview       string      `json:"preview"`
	Name          *string     `json:"name"`
	CWD           string      `json:"cwd"`
	ModelProvider string      `json:"modelProvider"`
	Model         string      `json:"model"`
	Turns         []codexTurn `json:"turns"`
}

type codexTurn struct {
	ID     string            `json:"id"`
	Status string            `json:"status"`
	Items  []json.RawMessage `json:"items"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
	StartedAt   float64 `json:"startedAt"`
	CompletedAt float64 `json:"completedAt"`
}

type commandApprovalParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Command  string `json:"command"`
	CWD      string `json:"cwd"`
	Reason   string `json:"reason"`
}

type fileApprovalParams struct {
	ThreadID  string `json:"threadId"`
	TurnID    string `json:"turnId"`
	ItemID    string `json:"itemId"`
	Reason    string `json:"reason"`
	GrantRoot string `json:"grantRoot"`
}

type permissionProfileParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	CWD      string `json:"cwd"`
	Reason   string `json:"reason"`
}

type toolInputParams struct {
	ThreadID  string `json:"threadId"`
	TurnID    string `json:"turnId"`
	ItemID    string `json:"itemId"`
	Questions []struct {
		ID       string `json:"id"`
		Header   string `json:"header"`
		Question string `json:"question"`
		Options  []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
	} `json:"questions"`
}

func toSession(thread codexThread) types.Session {
	title := strings.TrimSpace(thread.Preview)
	if thread.Name != nil && strings.TrimSpace(*thread.Name) != "" {
		title = strings.TrimSpace(*thread.Name)
	}
	return types.Session{ID: strings.TrimSpace(thread.ID), Title: title, Directory: strings.TrimSpace(thread.CWD)}
}

func itemRoleAndContent(raw json.RawMessage) (string, string, string) {
	var item struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Text    string `json:"text"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return "", "", ""
	}
	switch item.Type {
	case "agentMessage":
		return strings.TrimSpace(item.ID), "assistant", strings.TrimSpace(item.Text)
	case "userMessage":
		parts := make([]string, 0, len(item.Content))
		for _, part := range item.Content {
			if text := strings.TrimSpace(part.Text); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(item.ID), "user", strings.Join(parts, "\n")
	default:
		return "", "", ""
	}
}

func formatCommandPermission(params commandApprovalParams) string {
	parts := []string{"Codex requests command approval"}
	if strings.TrimSpace(params.Command) != "" {
		parts = append(parts, strings.TrimSpace(params.Command))
	}
	if strings.TrimSpace(params.Reason) != "" {
		parts = append(parts, strings.TrimSpace(params.Reason))
	}
	return strings.Join(parts, ": ")
}

func formatFilePermission(params fileApprovalParams) string {
	parts := []string{"Codex requests file change approval"}
	if strings.TrimSpace(params.GrantRoot) != "" {
		parts = append(parts, strings.TrimSpace(params.GrantRoot))
	}
	if strings.TrimSpace(params.Reason) != "" {
		parts = append(parts, strings.TrimSpace(params.Reason))
	}
	return strings.Join(parts, ": ")
}

func mapPermissionScope(reply types.PermissionReply) string {
	if reply == types.PermissionReplyAlways {
		return "session"
	}
	return "turn"
}

func mustMarshalRaw(value any) json.RawMessage {
	if value == nil {
		return nil
	}
	if raw, ok := value.(json.RawMessage); ok {
		return raw
	}
	data, _ := json.Marshal(value)
	return data
}

func decodeRaw(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, target)
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

func firstPositive(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func nowCompletedAt() float64 {
	return float64(time.Now().UnixNano()) / float64(time.Second)
}

func trimStringSlice(values []string) []string {
	resolved := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			resolved = append(resolved, trimmed)
		}
	}
	return resolved
}

func copyStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	target := make(map[string]string, len(source))
	for key, value := range source {
		target[key] = value
	}
	return target
}

func stringRawID(id json.RawMessage) string {
	var s string
	if json.Unmarshal(id, &s) == nil {
		return strings.TrimSpace(s)
	}
	var n int64
	if json.Unmarshal(id, &n) == nil {
		return fmt.Sprintf("%d", n)
	}
	return strings.TrimSpace(string(id))
}

var _ agent.Agent = (*Client)(nil)
