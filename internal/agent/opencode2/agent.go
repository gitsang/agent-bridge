package opencode2

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gitsang/agent-bridge/internal/bridge"
	"github.com/gitsang/agent-bridge/internal/types"
)

// Option customises the driver.
type Option func(*Options)

type Options struct {
	Logger   *slog.Logger
	Username string
	Password string
	Timeout  time.Duration
	Poll     time.Duration
}

type Agent struct {
	logger *slog.Logger
	client *client
	// timeout bounds one prompt turn; 0 means no imposed deadline.
	timeout time.Duration
	// poll is how often the turn is checked for completion after the prompt
	// is accepted.
	poll time.Duration
	// directories remembers which working directory each session belongs to,
	// and which timeout/settle context each running turn uses.
	turns turnRegistry
	logs  *localizer
}

// turnRegistry tracks in-flight prompts so cancellation can interrupt them.
type turnRegistry struct {
	mu    sync.Mutex
	turns map[string]*turnState
}

type turnState struct {
	sessionID string
	// baseline is the creation time of the user message this prompt appended,
	// in Unix milliseconds.
	baseline int64
	cancel   context.CancelFunc
	stop     chan struct{}
	stopOnce sync.Once
}

func WithLogger(logger *slog.Logger) Option {
	return func(target *Options) { target.Logger = logger }
}

func WithAuthentication(username, password string) Option {
	return func(target *Options) {
		target.Username = username
		target.Password = password
	}
}

func WithTimeout(timeout time.Duration) Option {
	return func(target *Options) {
		if timeout >= 0 {
			target.Timeout = timeout
		}
	}
}

func WithPollInterval(interval time.Duration) Option {
	return func(target *Options) {
		if interval > 0 {
			target.Poll = interval
		}
	}
}

func NewClient(baseURL string, options ...Option) *Agent {
	resolved := Options{
		Timeout: 30 * time.Minute,
		Poll:    time.Second,
	}
	for _, apply := range options {
		if apply == nil {
			continue
		}
		apply(&resolved)
	}

	agent := &Agent{
		logger:  loggerOrDefault(resolved.Logger),
		client:  newClient(baseURL, resolved.Username, resolved.Password, resolved.Timeout),
		timeout: resolved.Timeout,
		poll:    resolved.Poll,
		turns:   turnRegistry{turns: map[string]*turnState{}},
		logs:    newLocalizer(),
	}
	agent.client.localizer = agent.logs
	return agent
}

var _ bridge.Agent = (*Agent)(nil)

func (c *Agent) ListModels(ctx context.Context, directory string) ([]types.ModelInfo, error) {
	var resp envelope[[]modelInfo]
	query := locationQuery(nil, directory)
	if err := c.client.get(ctx, "/api/model", query, &resp); err != nil {
		return nil, err
	}

	models := make([]types.ModelInfo, 0, len(resp.Data))
	for _, item := range resp.Data {
		modelID := firstNonEmpty(item.ModelID, item.ID)
		providerID := strings.TrimSpace(item.ProviderID)
		if modelID == "" || providerID == "" {
			continue
		}
		providerName := providerID
		if provider, ok := c.providerName(ctx, directory, providerID); ok && provider != "" {
			providerName = provider
		}
		models = append(models, types.ModelInfo{
			ModelRef:     types.ModelRef{ProviderID: providerID, ModelID: modelID},
			ProviderName: providerName,
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

// providerName returns the configured display name for a provider ID.
func (c *Agent) providerName(ctx context.Context, directory, providerID string) (string, bool) {
	var documents []struct {
		Info struct {
			Providers map[string]struct {
				Name string `json:"name"`
			} `json:"providers"`
		} `json:"info"`
	}
	if err := c.client.get(ctx, "/api/config", locationQuery(nil, directory), &documents); err != nil {
		c.logger.Debug("opencode2 config read failed", slog.String("error", err.Error()))
		return "", false
	}
	for _, document := range documents {
		provider, ok := document.Info.Providers[providerID]
		if !ok {
			continue
		}
		if name := strings.TrimSpace(provider.Name); name != "" {
			return name, true
		}
	}
	return "", false
}

func (c *Agent) ResolveModel(ctx context.Context, spec, directory string) (types.ModelRef, error) {
	resolvedSpec := strings.TrimSpace(spec)
	if resolvedSpec == "" {
		return types.ModelRef{}, fmt.Errorf("model is required")
	}
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

func (c *Agent) ListAgents(ctx context.Context, directory string) ([]types.AgentInfo, error) {
	var resp envelope[[]agentInfo]
	if err := c.client.get(ctx, "/api/agent", locationQuery(nil, directory), &resp); err != nil {
		return nil, err
	}

	agents := make([]types.AgentInfo, 0, len(resp.Data))
	for _, item := range resp.Data {
		name := firstNonEmpty(strings.TrimSpace(item.ID), strings.TrimSpace(item.Name))
		if name == "" {
			continue
		}
		agents = append(agents, types.AgentInfo{
			Name:        name,
			Description: strings.TrimSpace(firstNonEmpty(item.Description, item.Name)),
			Mode:        strings.TrimSpace(item.Mode),
		})
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	return agents, nil
}

func (c *Agent) ListSessions(ctx context.Context, directory string) ([]types.Session, error) {
	query := url.Values{}
	query.Set("limit", "100")
	if resolvedDirectory := strings.TrimSpace(directory); resolvedDirectory != "" {
		query.Set("directory", resolvedDirectory)
	}

	var resp envelope[[]sessionInfo]
	if err := c.client.get(ctx, "/api/session", query, &resp); err != nil {
		return nil, err
	}

	sessions := make([]types.Session, 0, len(resp.Data))
	for _, item := range resp.Data {
		session, ok := c.toSession(item)
		if !ok {
			continue
		}
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

// ListAllSessions lists sessions across every project. The HttpApi scopes the
// session list to one location, so this asks each known directory and merges
// the results.
func (c *Agent) ListAllSessions(ctx context.Context) ([]types.Session, error) {
	directories := c.logs.all()
	if len(directories) == 0 {
		return c.ListSessions(ctx, "")
	}

	seen := map[string]struct{}{}
	sessions := make([]types.Session, 0, len(directories))
	for _, directory := range directories {
		scoped, err := c.ListSessions(ctx, directory)
		if err != nil {
			c.logger.Debug("opencode2 session list failed",
				slog.String("directory", directory),
				slog.String("error", err.Error()),
			)
			continue
		}
		for _, session := range scoped {
			if _, ok := seen[session.ID]; ok {
				continue
			}
			seen[session.ID] = struct{}{}
			sessions = append(sessions, session)
		}
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].UpdatedAt.Equal(sessions[j].UpdatedAt) {
			return sessions[i].ID < sessions[j].ID
		}
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})
	return sessions, nil
}

func (c *Agent) toSession(item sessionInfo) (types.Session, bool) {
	id := strings.TrimSpace(item.ID)
	if id == "" {
		return types.Session{}, false
	}
	directory := ""
	if item.Location != nil {
		directory = strings.TrimSpace(item.Location.Directory)
	}
	c.logs.set(id, directory)
	return types.Session{
		ID:        id,
		Title:     strings.TrimSpace(item.Title),
		Directory: directory,
		UpdatedAt: millisToTime(item.Time.Updated),
	}, true
}

func (c *Agent) GetSession(ctx context.Context, sessionID string) (*types.Session, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("opencode session id is required")
	}

	var resp envelope[sessionInfo]
	if err := c.client.get(ctx, "/api/session/"+url.PathEscape(resolvedSessionID), locationQuery(nil, c.logs.get(resolvedSessionID)), &resp); err != nil {
		return nil, err
	}
	session, ok := c.toSession(resp.Data)
	if !ok {
		return nil, nil
	}
	return &session, nil
}

func (c *Agent) CreateSession(ctx context.Context, request types.CreateSessionRequest) (*types.Session, error) {
	directory := strings.TrimSpace(request.Directory)
	body := createSessionBody{}
	if directory != "" {
		body.Location = &locationRef{Directory: directory}
	}
	if title := strings.TrimSpace(request.Title); title != "" {
		body.Title = &title
	}

	var resp envelope[sessionInfo]
	if err := c.client.post(ctx, "/api/session", nil, body, &resp); err != nil {
		return nil, err
	}
	session, ok := c.toSession(resp.Data)
	if !ok {
		return nil, fmt.Errorf("opencode returned an empty session id")
	}
	return &session, nil
}

func (c *Agent) GetMessages(ctx context.Context, sessionID string) ([]types.Message, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("opencode session id is required")
	}

	query := url.Values{}
	query.Set("order", "asc")
	query.Set("limit", "200")

	var resp envelope[[]messageInfo]
	if err := c.client.get(ctx, "/api/session/"+url.PathEscape(resolvedSessionID)+"/message", query, &resp); err != nil {
		return nil, err
	}

	messages := make([]types.Message, 0, len(resp.Data))
	for _, item := range resp.Data {
		role := ""
		switch item.Type {
		case "user":
			role = "user"
		case "assistant":
			role = "assistant"
		default:
			continue
		}
		messages = append(messages, types.Message{
			ID:          strings.TrimSpace(item.ID),
			SessionID:   resolvedSessionID,
			Role:        role,
			Content:     strings.TrimSpace(item.Text),
			CompletedAt: millisToSeconds(item.Time.Completed),
			Model:       toModelRef(item.Model),
		})
	}
	return messages, nil
}

func (c *Agent) GetLatestAssistantMessage(ctx context.Context, sessionID string) (*types.Message, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("opencode session id is required")
	}

	query := url.Values{}
	query.Set("order", "desc")
	query.Set("limit", "20")
	query.Set("type", "assistant")

	var resp envelope[[]messageInfo]
	if err := c.client.get(ctx, "/api/session/"+url.PathEscape(resolvedSessionID)+"/message", query, &resp); err != nil {
		return nil, err
	}

	for _, item := range resp.Data {
		if item.Time.Completed <= 0 {
			continue
		}
		return &types.Message{
			ID:          strings.TrimSpace(item.ID),
			SessionID:   resolvedSessionID,
			Role:        "assistant",
			CompletedAt: millisToSeconds(item.Time.Completed),
			Model:       toModelRef(item.Model),
		}, nil
	}
	return nil, nil
}

func (c *Agent) Prompt(ctx context.Context, sessionID string, prompt string, optfs ...types.PromptOptionFunc) (*types.PromptHandle, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("opencode session id is required")
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

	directory := firstNonEmpty(strings.TrimSpace(options.Directory), c.logs.get(resolvedSessionID))
	if resolvedDirectory := strings.TrimSpace(options.Directory); resolvedDirectory != "" {
		c.logs.set(resolvedSessionID, resolvedDirectory)
	}

	if resolvedAgent := strings.TrimSpace(options.Agent); resolvedAgent != "" {
		if err := c.client.post(ctx, "/api/session/"+url.PathEscape(resolvedSessionID)+"/agent", nil,
			map[string]string{"agent": resolvedAgent}, nil); err != nil {
			return nil, fmt.Errorf("switch opencode agent: %w", err)
		}
	}
	if !options.Model.IsZero() {
		ref, err := c.ResolveModel(ctx, options.Model.String(), directory)
		if err != nil {
			return nil, err
		}
		body := map[string]modelRef{"model": {
			ID:         ref.ModelID,
			ProviderID: ref.ProviderID,
			Variant:    "default",
		}}
		if err := c.client.post(ctx, "/api/session/"+url.PathEscape(resolvedSessionID)+"/model", nil, body, nil); err != nil {
			return nil, fmt.Errorf("switch opencode model: %w", err)
		}
	}

	// The user message this prompt appends marks where the turn starts. Waiting
	// for anything newer keeps an already-queued follow-up from settling the
	// turn before the agent answers.
	var accepted envelope[messageInfo]
	if err := c.client.post(ctx, "/api/session/"+url.PathEscape(resolvedSessionID)+"/prompt", nil,
		promptBody{Text: content}, &accepted); err != nil {
		return nil, err
	}
	baseline := accepted.Data.Time.Created

	promptCtx := ctx
	var cancel context.CancelFunc
	if c.timeout > 0 {
		promptCtx, cancel = context.WithTimeout(ctx, c.timeout)
	} else {
		promptCtx, cancel = context.WithCancel(ctx)
	}

	turn := &turnState{
		sessionID: resolvedSessionID,
		baseline:  baseline,
		cancel:    cancel,
		stop:      make(chan struct{}),
	}
	c.turns.add(resolvedSessionID, turn)

	doneCh := make(chan struct{})
	errCh := make(chan error, 1)

	go func() {
		defer cancel()
		err := c.awaitTurn(promptCtx, resolvedSessionID, turn)
		c.turns.remove(resolvedSessionID, turn)
		// A failed turn reports through Err only: closing Done as well would
		// race the bridge into treating it as a successful empty reply.
		if err != nil {
			errCh <- err
			return
		}
		close(doneCh)
	}()

	return types.NewPromptHandle(doneCh, errCh), nil
}

// awaitTurn waits until the session agent loop is idle, then reports any
// failure the session recorded.
func (c *Agent) awaitTurn(ctx context.Context, sessionID string, turn *turnState) error {
	ticker := time.NewTicker(c.poll)
	defer ticker.Stop()

	for {
		settled, err := c.turnSettled(ctx, sessionID, turn.baseline)
		if err != nil {
			if ctx.Err() != nil {
				c.interrupt(sessionID)
				return fmt.Errorf("opencode prompt aborted: %w", ctx.Err())
			}
			return err
		}
		if settled {
			return c.turnError(ctx, sessionID)
		}

		select {
		case <-ctx.Done():
			c.interrupt(sessionID)
			return fmt.Errorf("opencode prompt aborted: %w", ctx.Err())
		case <-turn.stop:
			c.interrupt(sessionID)
			return nil
		case <-ticker.C:
		}
	}
}

// turnSettled reports whether this prompt's turn finished: the agent loop is
// idle and the log holds an assistant or idle entry that came after the prompt
// was accepted. Requiring an outcome entry matters because the user message
// alone appears before the agent has answered.
func (c *Agent) turnSettled(ctx context.Context, sessionID string, baseline int64) (bool, error) {
	var session envelope[sessionInfo]
	if err := c.client.get(ctx, "/api/session/"+url.PathEscape(sessionID), locationQuery(nil, c.logs.get(sessionID)), &session); err != nil {
		return false, err
	}
	if session.Data.Time.Busy > session.Data.Time.Idle {
		return false, nil
	}

	query := url.Values{}
	query.Set("order", "desc")
	query.Set("limit", "10")
	var messages envelope[[]messageInfo]
	if err := c.client.get(ctx, "/api/session/"+url.PathEscape(sessionID)+"/message", query, &messages); err != nil {
		return false, err
	}
	for _, item := range messages.Data {
		if item.Time.Created <= baseline {
			continue
		}
		switch item.Type {
		case "assistant":
			// A streamed assistant message settles once it reports completion.
			if item.Time.Completed > 0 {
				return true, nil
			}
		case "idle":
			return true, nil
		}
	}
	return false, nil
}

// turnError reports a failed outcome recorded by the session or the newest
// assistant message.
func (c *Agent) turnError(ctx context.Context, sessionID string) error {
	query := url.Values{}
	query.Set("order", "desc")
	query.Set("limit", "5")

	var resp envelope[[]messageInfo]
	if err := c.client.get(ctx, "/api/session/"+url.PathEscape(sessionID)+"/message", query, &resp); err != nil {
		return nil
	}
	for _, item := range resp.Data {
		switch item.Type {
		case "assistant":
			if item.Finish == "error" && item.Error != nil {
				return errors.New(firstNonEmpty(item.Error.Message, "opencode turn failed"))
			}
			if item.Error != nil && item.Error.Message != "" {
				return errors.New(item.Error.Message)
			}
			return nil
		case "idle":
			if item.Outcome == "failed" && item.Error != nil {
				return errors.New(firstNonEmpty(item.Error.Message, "opencode turn failed"))
			}
			if item.Outcome == "interrupted" {
				return nil
			}
			return nil
		}
	}
	return nil
}

func (c *Agent) interrupt(sessionID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.client.post(ctx, "/api/session/"+url.PathEscape(sessionID)+"/interrupt", nil, nil, nil); err != nil {
		c.logger.Debug("opencode2 interrupt failed",
			slog.String("session_id", sessionID),
			slog.String("error", err.Error()),
		)
	}
}

func (c *Agent) PollMessagesAfter(ctx context.Context, sessionID string, afterCompletedAt float64, output types.MessageOutputOptions) ([]*types.Message, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("opencode session id is required")
	}

	query := url.Values{}
	query.Set("order", "asc")
	query.Set("limit", "200")
	query.Set("type", "assistant")

	var resp envelope[[]messageInfo]
	if err := c.client.get(ctx, "/api/session/"+url.PathEscape(resolvedSessionID)+"/message", query, &resp); err != nil {
		return nil, err
	}

	messages := make([]*types.Message, 0, len(resp.Data))
	for _, item := range resp.Data {
		if item.Time.Completed <= 0 {
			continue
		}
		completedAt := millisToSeconds(item.Time.Completed)
		if completedAt <= afterCompletedAt {
			continue
		}
		content := extractContent(item.Content, output)
		if content.Answer == "" && content.Reasoning == "" && content.Tools == "" && content.Diagnostics == "" {
			continue
		}
		messages = append(messages, &types.Message{
			ID:          strings.TrimSpace(item.ID),
			SessionID:   resolvedSessionID,
			Role:        "assistant",
			Content:     content.Answer,
			Reasoning:   content.Reasoning,
			Tools:       content.Tools,
			Diagnostics: content.Diagnostics,
			CompletedAt: completedAt,
			Model:       toModelRef(item.Model),
		})
	}
	sort.Slice(messages, func(i, j int) bool { return messages[i].CompletedAt < messages[j].CompletedAt })
	return messages, nil
}

func (c *Agent) ListPendingPermissions(ctx context.Context, sessionID string) ([]types.PermissionRequest, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("opencode session id is required")
	}

	var resp envelope[[]permissionRequest]
	if err := c.client.get(ctx, "/api/session/"+url.PathEscape(resolvedSessionID)+"/permission", locationQuery(nil, c.logs.get(resolvedSessionID)), &resp); err != nil {
		return nil, err
	}

	requests := make([]types.PermissionRequest, 0, len(resp.Data))
	for _, item := range resp.Data {
		requests = append(requests, types.PermissionRequest{
			ID:         strings.TrimSpace(item.ID),
			SessionID:  firstNonEmpty(strings.TrimSpace(item.SessionID), resolvedSessionID),
			Permission: firstNonEmpty(strings.TrimSpace(item.Message), strings.TrimSpace(item.Action)),
			Patterns:   trimStringSlice(item.Resources),
			Always:     trimStringSlice(item.Save),
			Metadata:   item.Metadata,
			Tool: types.InteractionTool{
				MessageID: metadataString(item.Metadata, "messageID"),
				CallID:    metadataString(item.Metadata, "callID"),
			},
		})
	}
	return requests, nil
}

func (c *Agent) ReplyPermission(ctx context.Context, sessionID string, requestID string, reply types.PermissionReply) error {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return fmt.Errorf("opencode session id is required")
	}
	resolvedRequestID := strings.TrimSpace(requestID)
	if resolvedRequestID == "" {
		return fmt.Errorf("permission request id is required")
	}

	resolvedReply := types.PermissionReply(strings.TrimSpace(string(reply)))
	switch resolvedReply {
	case types.PermissionReplyOnce, types.PermissionReplyAlways, types.PermissionReplyReject:
	default:
		return fmt.Errorf("unsupported permission reply: %s", resolvedReply)
	}

	err := c.client.post(ctx,
		"/api/session/"+url.PathEscape(resolvedSessionID)+"/permission/"+url.PathEscape(resolvedRequestID)+"/reply",
		nil,
		map[string]string{"decision": string(resolvedReply)},
		nil,
	)
	if isNotFound(err) {
		return types.ErrInteractionNoLongerPending
	}
	return err
}

func (c *Agent) ListPendingQuestions(ctx context.Context, sessionID string) ([]types.QuestionRequest, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("opencode session id is required")
	}

	var resp envelope[[]formInfo]
	if err := c.client.get(ctx, "/api/session/"+url.PathEscape(resolvedSessionID)+"/form", locationQuery(nil, c.logs.get(resolvedSessionID)), &resp); err != nil {
		return nil, err
	}

	requests := make([]types.QuestionRequest, 0, len(resp.Data))
	for _, item := range resp.Data {
		questions := make([]types.Question, 0, len(item.Fields))
		for _, field := range item.Fields {
			options := make([]string, 0, len(field.Options))
			for _, option := range field.Options {
				label := firstNonEmpty(strings.TrimSpace(option.Label), strings.TrimSpace(option.Value))
				if label != "" {
					options = append(options, label)
				}
			}
			questions = append(questions, types.Question{
				Text:     firstNonEmpty(strings.TrimSpace(field.Title), strings.TrimSpace(field.Key)),
				Options:  options,
				Multiple: field.Type == "multiselect",
			})
		}
		requests = append(requests, types.QuestionRequest{
			ID:        strings.TrimSpace(item.ID),
			SessionID: firstNonEmpty(strings.TrimSpace(item.SessionID), resolvedSessionID),
			Questions: questions,
		})
	}
	return requests, nil
}

// ReplyQuestion answers a form. Field keys are not exposed through
// QuestionRequest, so the mapping from answers to keys is read back from the
// form itself.
func (c *Agent) ReplyQuestion(ctx context.Context, sessionID string, requestID string, answers [][]string) error {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return fmt.Errorf("opencode session id is required")
	}
	resolvedRequestID := strings.TrimSpace(requestID)
	if resolvedRequestID == "" {
		return fmt.Errorf("question request id is required")
	}
	if len(answers) == 0 {
		return fmt.Errorf("question answers are required")
	}

	var resp envelope[formInfo]
	if err := c.client.get(ctx,
		"/api/session/"+url.PathEscape(resolvedSessionID)+"/form/"+url.PathEscape(resolvedRequestID),
		locationQuery(nil, c.logs.get(resolvedSessionID)),
		&resp,
	); err != nil {
		if isNotFound(err) {
			return types.ErrInteractionNoLongerPending
		}
		return err
	}

	answer := map[string]any{}
	for index, field := range resp.Data.Fields {
		if index >= len(answers) {
			break
		}
		values := trimStringSlice(answers[index])
		if len(values) == 0 {
			continue
		}
		if field.Type == "multiselect" {
			answer[field.Key] = values
			continue
		}
		answer[field.Key] = values[0]
	}
	if len(answer) == 0 {
		return fmt.Errorf("question answers are required")
	}

	err := c.client.post(ctx,
		"/api/session/"+url.PathEscape(resolvedSessionID)+"/form/"+url.PathEscape(resolvedRequestID)+"/reply",
		locationQuery(nil, c.logs.get(resolvedSessionID)),
		formReply{Answer: answer},
		nil,
	)
	if isNotFound(err) {
		return types.ErrInteractionNoLongerPending
	}
	return err
}

func (c *Agent) RejectQuestion(ctx context.Context, sessionID string, requestID string) error {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return fmt.Errorf("opencode session id is required")
	}
	resolvedRequestID := strings.TrimSpace(requestID)
	if resolvedRequestID == "" {
		return fmt.Errorf("question request id is required")
	}

	err := c.client.do(ctx, http.MethodDelete,
		"/api/session/"+url.PathEscape(resolvedSessionID)+"/form/"+url.PathEscape(resolvedRequestID),
		locationQuery(nil, c.logs.get(resolvedSessionID)),
		nil, nil,
	)
	if isNotFound(err) {
		return types.ErrInteractionNoLongerPending
	}
	return err
}

// turnRegistry helpers

func (r *turnRegistry) add(sessionID string, turn *turnState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.turns == nil {
		r.turns = map[string]*turnState{}
	}
	r.turns[sessionID] = turn
}

func (r *turnRegistry) remove(sessionID string, turn *turnState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.turns[sessionID] == turn {
		delete(r.turns, sessionID)
	}
}

// localizer directory index helpers

func (l *localizer) all() []string {
	seen := map[string]struct{}{}
	directories := make([]string, 0, len(l.directories))
	for _, directory := range l.directories {
		if _, ok := seen[directory]; ok {
			continue
		}
		seen[directory] = struct{}{}
		directories = append(directories, directory)
	}
	sort.Strings(directories)
	return directories
}

func toModelRef(model *modelRef) types.ModelRef {
	if model == nil {
		return types.ModelRef{}
	}
	return types.ModelRef{
		ProviderID: strings.TrimSpace(model.ProviderID),
		ModelID:    strings.TrimSpace(model.ID),
	}
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return text
}
