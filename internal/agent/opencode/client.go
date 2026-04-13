package opencode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/gitsang/agent-bridge/internal/agent"
	ocsdk "github.com/sst/opencode-sdk-go"
	"github.com/sst/opencode-sdk-go/option"
)

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

func (c *Client) GetSessionMessages(ctx context.Context, sessionID string) ([]agent.Message, error) {
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
			ID:   strings.TrimSpace(msg.Info.ID),
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
		defer close(doneCh)
		_, err := c.client.Session.Prompt(ctx, resolvedSessionID, params)
		if err != nil {
			errCh <- err
		}
	}()

	return agent.NewPromptHandle(doneCh, errCh), nil
}

func (c *Client) PollMessagesAfter(ctx context.Context, sessionID string, afterCompletedAt float64) ([]*agent.Message, error) {
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
		result, err := c.buildPromptResult(ctx, sessionID, candidate.Time.Completed, resp)
		if err != nil {
			retErr = err
			return nil, retErr
		}
		results = append(results, result)
	}

	return results, nil
}

func (c *Client) buildPromptResult(ctx context.Context, fallbackSessionID string, completedAt float64, response *ocsdk.SessionMessageResponse) (*agent.Message, error) {
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
		Content:   extractReply(response.Parts),
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

func extractReply(parts []ocsdk.Part) string {
	builder := strings.Builder{}

	appendText := func(prefix, text string) {
		if text = strings.TrimSpace(text); text == "" {
			return
		}
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		if prefix != "" {
			builder.WriteString(prefix)
			builder.WriteString(" ")
		}
		builder.WriteString(text)
	}

	for _, part := range parts {
		switch part.Type {
		case ocsdk.PartTypeText:
			appendText("", part.Text)
		case ocsdk.PartTypeReasoning:
			appendText("[reasoning]", part.Text)
		case ocsdk.PartTypeFile:
			appendText("[file:", part.Filename+"]")
		case ocsdk.PartTypeTool:
			state, ok := part.State.(ocsdk.ToolPartState)
			if !ok {
				appendText("[tool:", part.Tool+"]")
				break
			}
			var inputStr string
			if b, err := json.Marshal(state.Input); err == nil {
				inputStr = string(b)
			}
			appendText(fmt.Sprintf("[tool: %s]", part.Tool), fmt.Sprintf("%s\n\n%s", inputStr, strings.TrimSpace(state.Output)))
		case ocsdk.PartTypeStepStart:
		case ocsdk.PartTypeStepFinish:
		case ocsdk.PartTypeSnapshot:
			appendText("[snapshot]", part.Snapshot)
		case ocsdk.PartTypePatch:
			if files, ok := part.Files.([]string); ok {
				appendText("[patch]", strings.Join(files, ", "))
			}
		case ocsdk.PartTypeAgent:
			appendText("[agent:", part.Name+"]")
		case ocsdk.PartTypeRetry:
			if e, ok := part.Error.(ocsdk.PartRetryPartError); ok {
				appendText("[retry]", e.Data.Message)
			}
		}
	}

	return strings.TrimSpace(builder.String())
}
