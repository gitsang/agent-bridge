package opencode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gitsang/agent-bridge/internal/agent"
	ocsdk "github.com/sst/opencode-sdk-go"
	"github.com/sst/opencode-sdk-go/option"
)

type rawInteractionTool struct {
	MessageID string `json:"messageID"`
	CallID    string `json:"callID"`
}

type rawPermissionRequest struct {
	ID         string             `json:"id"`
	SessionID  string             `json:"sessionID"`
	Permission string             `json:"permission"`
	Title      string             `json:"title"`
	Type       string             `json:"type"`
	Pattern    any                `json:"pattern"`
	Patterns   []string           `json:"patterns"`
	Always     []string           `json:"always"`
	Metadata   map[string]any     `json:"metadata"`
	MessageID  string             `json:"messageID"`
	CallID     string             `json:"callID"`
	Tool       rawInteractionTool `json:"tool"`
}

type rawQuestion struct {
	Text     string              `json:"text"`
	Question string              `json:"question"`
	Options  []rawQuestionOption `json:"options"`
	Multiple bool                `json:"multiple"`
}

type rawQuestionOption string

func (o *rawQuestionOption) UnmarshalJSON(data []byte) error {
	var label string
	if err := json.Unmarshal(data, &label); err == nil {
		*o = rawQuestionOption(label)
		return nil
	}

	var object struct {
		Label string `json:"label"`
	}
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	*o = rawQuestionOption(object.Label)
	return nil
}

type rawQuestionRequest struct {
	ID        string             `json:"id"`
	SessionID string             `json:"sessionID"`
	Questions []rawQuestion      `json:"questions"`
	Tool      rawInteractionTool `json:"tool"`
}

type rawQuestionReply struct {
	Answers [][]string `json:"answers"`
}

type Option func(*Options)

type Options struct {
	Logger   *slog.Logger
	Username string
	Password string
	Timeout  time.Duration
}

type Client struct {
	logger  *slog.Logger
	client  *ocsdk.Client
	timeout time.Duration
}

func WithLogger(logger *slog.Logger) Option {
	return func(target *Options) {
		target.Logger = logger
	}
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

func NewClient(baseURL string, options ...Option) *Client {
	resolved := Options{Timeout: 10 * time.Minute}

	for _, apply := range options {
		if apply == nil {
			continue
		}
		apply(&resolved)
	}

	if resolved.Logger == nil {
		resolved.Logger = slog.Default()
	}

	timeout := resolved.Timeout
	if timeout < 0 {
		timeout = 10 * time.Minute
	}

	sdkOptions := []option.RequestOption{option.WithBaseURL(baseURL)}
	if resolved.Username != "" || resolved.Password != "" {
		credential := fmt.Sprintf("%s:%s", resolved.Username, resolved.Password)
		authValue := "Basic " + base64.StdEncoding.EncodeToString([]byte(credential))
		sdkOptions = append(sdkOptions, option.WithHeader("Authorization", authValue))
	}

	sdkClient := ocsdk.NewClient(sdkOptions...)
	return &Client{logger: resolved.Logger, client: sdkClient, timeout: timeout}
}

func (c *Client) ListSessions(ctx context.Context, directory string) ([]agent.Session, error) {
	params := ocsdk.SessionListParams{}
	if strings.TrimSpace(directory) != "" {
		params.Directory = ocsdk.F(strings.TrimSpace(directory))
	}

	resp, err := c.client.Session.List(ctx, params)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return []agent.Session{}, nil
	}

	sessions := make([]agent.Session, 0, len(*resp))
	for _, s := range *resp {
		sessions = append(sessions, toSession(s))
	}
	return sessions, nil
}

func (c *Client) ListModels(ctx context.Context, directory string) ([]agent.ModelInfo, error) {
	params := ocsdk.AppProvidersParams{}
	if strings.TrimSpace(directory) != "" {
		params.Directory = ocsdk.F(strings.TrimSpace(directory))
	}

	resp, err := c.client.App.Providers(ctx, params)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return []agent.ModelInfo{}, nil
	}

	models := make([]agent.ModelInfo, 0)
	for _, provider := range resp.Providers {
		providerID := strings.TrimSpace(provider.ID)
		providerName := strings.TrimSpace(provider.Name)
		for modelID, model := range provider.Models {
			resolvedModelID := strings.TrimSpace(model.ID)
			if resolvedModelID == "" {
				resolvedModelID = strings.TrimSpace(modelID)
			}
			if resolvedModelID == "" || providerID == "" {
				continue
			}
			models = append(models, agent.ModelInfo{
				ModelRef:     agent.ModelRef{ProviderID: providerID, ModelID: resolvedModelID},
				ProviderName: providerName,
				ModelName:    strings.TrimSpace(model.Name),
			})
		}
	}

	sort.Slice(models, func(i, j int) bool {
		if models[i].ProviderID == models[j].ProviderID {
			return models[i].ModelID < models[j].ModelID
		}
		return models[i].ProviderID < models[j].ProviderID
	})

	return models, nil
}

func (c *Client) ListAgents(ctx context.Context, directory string) ([]agent.AgentInfo, error) {
	params := ocsdk.AgentListParams{}
	if strings.TrimSpace(directory) != "" {
		params.Directory = ocsdk.F(strings.TrimSpace(directory))
	}

	resp, err := c.client.Agent.List(ctx, params)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return []agent.AgentInfo{}, nil
	}

	agents := make([]agent.AgentInfo, 0, len(*resp))
	for _, item := range *resp {
		resolvedName := strings.TrimSpace(item.Name)
		if resolvedName == "" {
			continue
		}
		agents = append(agents, agent.AgentInfo{
			Name:        resolvedName,
			Description: strings.TrimSpace(item.Description),
			Mode:        strings.TrimSpace(string(item.Mode)),
		})
	}

	sort.Slice(agents, func(i, j int) bool {
		return agents[i].Name < agents[j].Name
	})

	return agents, nil
}

func (c *Client) GetSession(ctx context.Context, sessionID string) (*agent.Session, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("opencode session id is required")
	}

	resp, err := c.client.Session.Get(ctx, resolvedSessionID, ocsdk.SessionGetParams{})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	s := toSession(*resp)
	return &s, nil
}

func (c *Client) getSessionMessages(ctx context.Context, sessionID string) ([]ocsdk.SessionMessagesResponse, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("opencode session id is required")
	}

	resp, err := c.client.Session.Messages(ctx, resolvedSessionID, ocsdk.SessionMessagesParams{})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return []ocsdk.SessionMessagesResponse{}, nil
	}

	return *resp, nil
}

func (c *Client) GetMessages(ctx context.Context, sessionID string) ([]agent.Message, error) {
	raw, err := c.getSessionMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	messages := make([]agent.Message, 0, len(raw))
	for i, msg := range raw {
		c.logger.Debug("session message response",
			slog.String("session_id", sessionID),
			slog.Int("index", i),
			slog.Any("info", msg.Info),
			slog.Any("parts", msg.Parts),
		)
		messages = append(messages, agent.Message{
			ID: strings.TrimSpace(msg.Info.ID),
			Model: agent.ModelRef{
				ProviderID: strings.TrimSpace(msg.Info.ProviderID),
				ModelID:    strings.TrimSpace(msg.Info.ModelID),
			},
			Role: string(msg.Info.Role),
		})
	}

	return messages, nil
}

func (c *Client) GetLatestAssistantMessage(ctx context.Context, sessionID string) (*agent.Message, error) {
	raw, err := c.getSessionMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	for i := len(raw) - 1; i >= 0; i-- {
		if raw[i].Info.Role != "assistant" {
			continue
		}
		assistant, ok := raw[i].Info.AsUnion().(ocsdk.AssistantMessage)
		if !ok {
			continue
		}
		if assistant.Time.Completed <= 0 {
			continue
		}
		msg := agent.Message{
			ID: strings.TrimSpace(raw[i].Info.ID),
			Model: agent.ModelRef{
				ProviderID: strings.TrimSpace(raw[i].Info.ProviderID),
				ModelID:    strings.TrimSpace(raw[i].Info.ModelID),
			},
			Role:        string(raw[i].Info.Role),
			CompletedAt: assistant.Time.Completed,
		}
		c.logger.Debug("session latest assistant message",
			slog.String("session_id", sessionID),
			slog.Any("info", raw[i].Info),
			slog.Any("parts", raw[i].Parts),
		)
		return &msg, nil
	}

	return nil, nil
}

func (c *Client) CreateSession(ctx context.Context, request agent.CreateSessionRequest) (*agent.Session, error) {
	params := ocsdk.SessionNewParams{}
	if strings.TrimSpace(request.Directory) != "" {
		params.Directory = ocsdk.F(strings.TrimSpace(request.Directory))
	}
	if strings.TrimSpace(request.Title) != "" {
		params.Title = ocsdk.F(strings.TrimSpace(request.Title))
	}

	resp, err := c.client.Session.New(ctx, params)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	s := toSession(*resp)
	return &s, nil
}

func (c *Client) Prompt(ctx context.Context, sessionID string, prompt string, optfs ...agent.PromptOptionFunc) (*agent.PromptHandle, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("opencode session id is required")
	}
	resolvedContent := strings.TrimSpace(prompt)
	if resolvedContent == "" {
		return nil, fmt.Errorf("message content is required")
	}

	resolvedOptions := agent.PromptOptions{}
	for _, apply := range optfs {
		if apply == nil {
			continue
		}
		apply(&resolvedOptions)
	}

	parts := []ocsdk.SessionPromptParamsPartUnion{
		ocsdk.TextPartInputParam{
			Type: ocsdk.F(ocsdk.TextPartInputTypeText),
			Text: ocsdk.F(resolvedContent),
		},
	}

	params := ocsdk.SessionPromptParams{Parts: ocsdk.F(parts)}
	resolvedDirectory := strings.TrimSpace(resolvedOptions.Directory)
	if resolvedDirectory != "" {
		params.Directory = ocsdk.F(resolvedDirectory)
	}

	if !resolvedOptions.Model.IsZero() {
		ref, err := c.ResolveModel(ctx, resolvedOptions.Model.String(), resolvedDirectory)
		if err != nil {
			return nil, err
		}
		params.Model = ocsdk.F(ocsdk.SessionPromptParamsModel{
			ProviderID: ocsdk.F(ref.ProviderID),
			ModelID:    ocsdk.F(ref.ModelID),
		})
	}

	resolvedAgent := strings.TrimSpace(resolvedOptions.Agent)
	if resolvedAgent != "" {
		params.Agent = ocsdk.F(resolvedAgent)
	}

	doneCh := make(chan struct{})
	errCh := make(chan error, 1)

	go func() {
		promptCtx := ctx
		var cancel context.CancelFunc
		requestOptions := []option.RequestOption{}
		if c.timeout > 0 {
			promptCtx, cancel = context.WithTimeout(ctx, c.timeout)
			requestOptions = append(requestOptions, option.WithRequestTimeout(c.timeout))
		}
		if cancel != nil {
			defer cancel()
		}

		_, err := c.client.Session.Prompt(promptCtx, resolvedSessionID, params, requestOptions...)
		if err != nil {
			if c.timeout > 0 && errors.Is(promptCtx.Err(), context.DeadlineExceeded) {
				_, _ = c.client.Session.Abort(ctx, resolvedSessionID, ocsdk.SessionAbortParams{})
				err = fmt.Errorf("opencode prompt timed out after %s: %w", c.timeout, err)
			}
			errCh <- err
			return
		}
		close(doneCh)
	}()

	return agent.NewPromptHandle(doneCh, errCh), nil
}

func (c *Client) PollMessagesAfter(ctx context.Context, sessionID string, afterCompletedAt float64, output agent.MessageOutputOptions) ([]*agent.Message, error) {
	var results []*agent.Message
	var retErr error
	logger := c.logger.With(
		"session_id", sessionID,
		"after_completed_at", afterCompletedAt,
	)
	defer func() {
		logger.Debug("poll messages after",
			"results", len(results),
			"err", retErr,
		)
	}()

	messages, err := c.client.Session.Messages(ctx, sessionID, ocsdk.SessionMessagesParams{})
	if err != nil {
		retErr = err
		return nil, retErr
	}
	if messages == nil {
		return nil, nil
	}

	var candidates []ocsdk.AssistantMessage
	for _, message := range *messages {
		assistant, ok := message.Info.AsUnion().(ocsdk.AssistantMessage)
		if !ok {
			continue
		}
		if assistant.Time.Completed <= afterCompletedAt {
			continue
		}
		if assistant.Error.Name != "" {
			retErr = fmt.Errorf("prompt failed: %s", assistant.Error.Name)
			return nil, retErr
		}
		if assistant.Time.Completed <= 0 {
			continue
		}
		candidates = append(candidates, assistant)
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Time.Completed < candidates[j].Time.Completed
	})

	results = make([]*agent.Message, 0, len(candidates))
	for _, candidate := range candidates {
		resp, err := c.client.Session.Message(ctx, sessionID, candidate.ID, ocsdk.SessionMessageParams{})
		if err != nil {
			retErr = err
			return nil, retErr
		}
		logger.With("message_id", candidate.ID).Debug("poll messages after: raw message response",
			"response", resp,
		)
		result, err := c.buildPromptResult(ctx, sessionID, candidate.Time.Completed, resp, output)
		if err != nil {
			retErr = err
			return nil, retErr
		}
		if result.Content == "" {
			continue
		}
		results = append(results, result)
	}

	return results, nil
}

func (c *Client) ListPendingPermissions(ctx context.Context, sessionID string) ([]agent.PermissionRequest, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("opencode session id is required")
	}

	var raw []rawPermissionRequest
	if err := c.client.Get(ctx, "/permission", nil, &raw); err != nil {
		return nil, err
	}

	requests := make([]agent.PermissionRequest, 0, len(raw))
	for _, item := range raw {
		if strings.TrimSpace(item.SessionID) != resolvedSessionID {
			continue
		}
		messageID := strings.TrimSpace(item.Tool.MessageID)
		if messageID == "" {
			messageID = strings.TrimSpace(item.MessageID)
		}
		callID := strings.TrimSpace(item.Tool.CallID)
		if callID == "" {
			callID = strings.TrimSpace(item.CallID)
		}
		requests = append(requests, agent.PermissionRequest{
			ID:         strings.TrimSpace(item.ID),
			SessionID:  strings.TrimSpace(item.SessionID),
			Permission: resolvePermissionLabel(item),
			Patterns:   resolvePermissionPatterns(item),
			Always:     trimStringSlice(item.Always),
			Metadata:   item.Metadata,
			Tool: agent.InteractionTool{
				MessageID: messageID,
				CallID:    callID,
			},
		})
	}

	return requests, nil
}

func (c *Client) ReplyPermission(ctx context.Context, sessionID string, requestID string, reply agent.PermissionReply) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("opencode session id is required")
	}
	resolvedRequestID := strings.TrimSpace(requestID)
	if resolvedRequestID == "" {
		return fmt.Errorf("permission request id is required")
	}
	resolvedReply := agent.PermissionReply(strings.TrimSpace(string(reply)))
	if resolvedReply != agent.PermissionReplyOnce && resolvedReply != agent.PermissionReplyAlways && resolvedReply != agent.PermissionReplyReject {
		return fmt.Errorf("unsupported permission reply: %s", resolvedReply)
	}

	response := ocsdk.SessionPermissionRespondParamsResponse(resolvedReply)
	ok, err := c.client.Session.Permissions.Respond(ctx, strings.TrimSpace(sessionID), resolvedRequestID, ocsdk.SessionPermissionRespondParams{Response: ocsdk.F(response)})
	if isNotFound(err) || ((ok == nil || !*ok) && err == nil) {
		return agent.ErrInteractionNoLongerPending
	}
	return err
}

func (c *Client) ListPendingQuestions(ctx context.Context, sessionID string) ([]agent.QuestionRequest, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("opencode session id is required")
	}

	var raw []rawQuestionRequest
	if err := c.client.Get(ctx, "/question", nil, &raw); err != nil {
		return nil, err
	}

	requests := make([]agent.QuestionRequest, 0, len(raw))
	for _, item := range raw {
		if strings.TrimSpace(item.SessionID) != resolvedSessionID {
			continue
		}
		questions := make([]agent.Question, 0, len(item.Questions))
		for _, question := range item.Questions {
			questions = append(questions, agent.Question{
				Text:     firstNonEmpty(strings.TrimSpace(question.Question), strings.TrimSpace(question.Text)),
				Options:  trimQuestionOptions(question.Options),
				Multiple: question.Multiple,
			})
		}
		requests = append(requests, agent.QuestionRequest{
			ID:        strings.TrimSpace(item.ID),
			SessionID: strings.TrimSpace(item.SessionID),
			Questions: questions,
			Tool: agent.InteractionTool{
				MessageID: strings.TrimSpace(item.Tool.MessageID),
				CallID:    strings.TrimSpace(item.Tool.CallID),
			},
		})
	}

	return requests, nil
}

func (c *Client) ReplyQuestion(ctx context.Context, sessionID string, requestID string, answers [][]string) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("opencode session id is required")
	}
	resolvedRequestID := strings.TrimSpace(requestID)
	if resolvedRequestID == "" {
		return fmt.Errorf("question request id is required")
	}
	if len(answers) == 0 {
		return fmt.Errorf("question answers are required")
	}

	resolvedAnswers := make([][]string, 0, len(answers))
	for _, answerSet := range answers {
		resolvedAnswerSet := trimStringSlice(answerSet)
		if len(resolvedAnswerSet) == 0 {
			return fmt.Errorf("question answers are required")
		}
		resolvedAnswers = append(resolvedAnswers, resolvedAnswerSet)
	}

	var ok bool
	err := c.client.Post(ctx, fmt.Sprintf("/question/%s/reply", resolvedRequestID), rawQuestionReply{Answers: resolvedAnswers}, &ok)
	if isNotFound(err) || (!ok && err == nil) {
		return agent.ErrInteractionNoLongerPending
	}
	return err
}

func (c *Client) RejectQuestion(ctx context.Context, sessionID string, requestID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("opencode session id is required")
	}
	resolvedRequestID := strings.TrimSpace(requestID)
	if resolvedRequestID == "" {
		return fmt.Errorf("question request id is required")
	}

	var ok bool
	err := c.client.Post(ctx, fmt.Sprintf("/question/%s/reject", resolvedRequestID), nil, &ok)
	if isNotFound(err) || (!ok && err == nil) {
		return agent.ErrInteractionNoLongerPending
	}
	return err
}

func (c *Client) buildPromptResult(ctx context.Context, fallbackSessionID string, completedAt float64, response *ocsdk.SessionMessageResponse, output agent.MessageOutputOptions) (*agent.Message, error) {
	if response == nil {
		return nil, fmt.Errorf("empty message response")
	}

	assistant, ok := response.Info.AsUnion().(ocsdk.AssistantMessage)
	if !ok {
		return nil, fmt.Errorf("unexpected message role: %s", response.Info.Role)
	}
	if assistant.Error.Name != "" {
		return nil, fmt.Errorf("prompt failed: %s", assistant.Error.Name)
	}

	resultSessionID := strings.TrimSpace(assistant.SessionID)
	if resultSessionID == "" {
		resultSessionID = strings.TrimSpace(fallbackSessionID)
	}

	result := &agent.Message{
		Content:   extractReply(response.Parts, output),
		SessionID: resultSessionID,
		Model: agent.ModelRef{
			ProviderID: strings.TrimSpace(assistant.ProviderID),
			ModelID:    strings.TrimSpace(assistant.ModelID),
		},
		CompletedAt: completedAt,
	}

	return result, nil
}

func (c *Client) ResolveModel(ctx context.Context, spec, directory string) (agent.ModelRef, error) {
	resolvedModel := strings.TrimSpace(spec)
	if resolvedModel == "" {
		return agent.ModelRef{}, fmt.Errorf("model is required")
	}

	if strings.Contains(resolvedModel, "/") {
		pair := strings.SplitN(resolvedModel, "/", 2)
		providerID := strings.TrimSpace(pair[0])
		modelID := strings.TrimSpace(pair[1])
		if providerID == "" || modelID == "" {
			return agent.ModelRef{}, fmt.Errorf("invalid model format: %s", resolvedModel)
		}
		return agent.ModelRef{ProviderID: providerID, ModelID: modelID}, nil
	}

	models, err := c.ListModels(ctx, directory)
	if err != nil {
		return agent.ModelRef{}, err
	}

	matches := make([]agent.ModelInfo, 0, 4)
	for _, candidate := range models {
		if strings.EqualFold(candidate.ModelID, resolvedModel) {
			matches = append(matches, candidate)
		}
	}

	if len(matches) == 0 {
		return agent.ModelRef{}, fmt.Errorf("model not found: %s", resolvedModel)
	}
	if len(matches) > 1 {
		return agent.ModelRef{}, fmt.Errorf("ambiguous model %s, use provider/model", resolvedModel)
	}

	return matches[0].ModelRef, nil
}

func toSession(s ocsdk.Session) agent.Session {
	return agent.Session{
		ID:        strings.TrimSpace(s.ID),
		Title:     strings.TrimSpace(s.Title),
		Directory: strings.TrimSpace(s.Directory),
	}
}

func trimQuestionOptions(values []rawQuestionOption) []string {
	if len(values) == 0 {
		return nil
	}
	resolved := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(string(value))
		if trimmed == "" {
			continue
		}
		resolved = append(resolved, trimmed)
	}
	return resolved
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func trimStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	resolved := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		resolved = append(resolved, trimmed)
	}
	return resolved
}

func resolvePermissionLabel(request rawPermissionRequest) string {
	if trimmed := strings.TrimSpace(request.Permission); trimmed != "" {
		return trimmed
	}
	if trimmed := strings.TrimSpace(request.Title); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(request.Type)
}

func resolvePermissionPatterns(request rawPermissionRequest) []string {
	patterns := trimStringSlice(request.Patterns)
	if len(patterns) > 0 {
		return patterns
	}
	switch pattern := request.Pattern.(type) {
	case string:
		return trimStringSlice([]string{pattern})
	case []any:
		values := make([]string, 0, len(pattern))
		for _, value := range pattern {
			if text, ok := value.(string); ok {
				values = append(values, text)
			}
		}
		return trimStringSlice(values)
	}
	return nil
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *ocsdk.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.StatusCode == http.StatusNotFound
}

func extractReply(parts []ocsdk.Part, output agent.MessageOutputOptions) string {
	builder := strings.Builder{}

	for _, part := range parts {
		switch part.Type {
		case ocsdk.PartTypeText:
			if !output.Includes(agent.MessageContentAnswer) {
				break
			}
			text := strings.TrimSpace(part.Text)
			if text == "" {
				break
			}
			builder.WriteString("\n" + text)
		case ocsdk.PartTypeReasoning:
			if !output.Includes(agent.MessageContentReasoning) {
				break
			}
			text := strings.TrimSpace(part.Text)
			if text == "" {
				break
			}
			fmt.Fprintf(&builder, "\n<thinking>\n%s\n</thinking>", text)
		case ocsdk.PartTypeFile:
			if !output.Includes(agent.MessageContentArtifactFile) {
				break
			}
			filename := strings.TrimSpace(part.Filename)
			if filename == "" {
				break
			}
			fmt.Fprintf(&builder, "\n<file name=%s />", filename)
		case ocsdk.PartTypeTool:
			if !output.Includes(agent.MessageContentActionTool) {
				break
			}
			state, ok := part.State.(ocsdk.ToolPartState)
			if !ok {
				tool := strings.TrimSpace(part.Tool)
				if tool == "" {
					break
				}
				fmt.Fprintf(&builder, "\n<tool name=%s />", tool)
				break
			}

			if b, err := json.Marshal(state.Input); err == nil {
				inputStr := string(b)
				fmt.Fprintf(&builder, "\n<tool name=\"%s\" type=\"input\">\n%s\n</tool>", part.Tool, inputStr)
			}
			outputStr := strings.TrimSpace(state.Output)
			fmt.Fprintf(&builder, "\n<tool name=\"%s\" type=\"output\">\n%s\n</tool>", part.Tool, outputStr)
		case ocsdk.PartTypeStepStart:
		case ocsdk.PartTypeStepFinish:
		case ocsdk.PartTypeSnapshot:
			if !output.Includes(agent.MessageContentArtifactState) {
				break
			}
			text := strings.TrimSpace(part.Snapshot)
			if text == "" {
				break
			}
			fmt.Fprintf(&builder, "\n<snapshot>%s</snapshot>", text)
		case ocsdk.PartTypePatch:
			if !output.Includes(agent.MessageContentArtifactPatch) {
				break
			}
			if files, ok := part.Files.([]string); ok {
				text := strings.TrimSpace(strings.Join(files, ", "))
				if text == "" {
					break
				}
				fmt.Fprintf(&builder, "\n<patch>%s</patch>", text)
			}
		case ocsdk.PartTypeAgent:
			if !output.Includes(agent.MessageContentActionAgent) {
				break
			}
			name := strings.TrimSpace(part.Name)
			if name == "" {
				break
			}
			fmt.Fprintf(&builder, "\n<agent name=\"%s\" />", name)
		case ocsdk.PartTypeRetry:
			if !output.Includes(agent.MessageContentDiagnostic) {
				break
			}
			if e, ok := part.Error.(ocsdk.PartRetryPartError); ok {
				text := strings.TrimSpace(e.Data.Message)
				if text == "" {
					break
				}
				fmt.Fprintf(&builder, "\n<retry>%s</retry>", text)
			}
		}
	}

	return strings.TrimSpace(builder.String())
}
