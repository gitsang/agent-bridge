package opencode

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	ocsdk "github.com/sst/opencode-sdk-go"
	"github.com/sst/opencode-sdk-go/option"
)

type Session = ocsdk.Session

type Option func(*Options)

type Options struct {
	Logger   *slog.Logger
	Username string
	Password string
	Timeout  time.Duration
}

type PromptRequest struct {
	SessionID string
	Content   string
	Model     string
	Agent     string
	Workdir   string
}

type PromptHandle struct {
	done <-chan struct{}
	err  <-chan error
}

func NewPromptHandle(done <-chan struct{}, err <-chan error) *PromptHandle {
	return &PromptHandle{done: done, err: err}
}

func (h *PromptHandle) Done() <-chan struct{} {
	return h.done
}

func (h *PromptHandle) Err() <-chan error {
	return h.err
}

type CreateSessionRequest struct {
	Title   string
	Workdir string
}

type PromptResult struct {
	Reply       string
	SessionID   string
	Title       string
	Workdir     string
	ProviderID  string
	ModelID     string
	Mode        string
	CompletedAt float64
}

type SessionMessage struct {
	ID          string
	ProviderID  string
	ModelID     string
	Mode        string
	Role        string
	CompletedAt float64
}

type ModelInfo struct {
	ProviderID string
	ModelID    string
	Name       string
}

type AgentInfo struct {
	Name        string
	Description string
	Mode        string
}

type Client struct {
	logger  *slog.Logger
	client  *ocsdk.Client
	timeout time.Duration
}

const PromptPollInterval = 2 * time.Second

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

func (c *Client) ListSessions(ctx context.Context, workdir string) ([]ocsdk.Session, error) {
	params := ocsdk.SessionListParams{}
	if strings.TrimSpace(workdir) != "" {
		params.Directory = ocsdk.F(strings.TrimSpace(workdir))
	}

	resp, err := c.client.Session.List(ctx, params)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return []ocsdk.Session{}, nil
	}

	return *resp, nil
}

func (c *Client) ListModels(ctx context.Context, workdir string) ([]ModelInfo, error) {
	params := ocsdk.AppProvidersParams{}
	if strings.TrimSpace(workdir) != "" {
		params.Directory = ocsdk.F(strings.TrimSpace(workdir))
	}

	resp, err := c.client.App.Providers(ctx, params)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return []ModelInfo{}, nil
	}

	models := make([]ModelInfo, 0)
	for _, provider := range resp.Providers {
		providerID := strings.TrimSpace(provider.ID)
		for modelID, model := range provider.Models {
			resolvedModelID := strings.TrimSpace(model.ID)
			if resolvedModelID == "" {
				resolvedModelID = strings.TrimSpace(modelID)
			}
			if resolvedModelID == "" || providerID == "" {
				continue
			}
			models = append(models, ModelInfo{
				ProviderID: providerID,
				ModelID:    resolvedModelID,
				Name:       strings.TrimSpace(model.Name),
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

func (c *Client) ListAgents(ctx context.Context, workdir string) ([]AgentInfo, error) {
	params := ocsdk.AgentListParams{}
	if strings.TrimSpace(workdir) != "" {
		params.Directory = ocsdk.F(strings.TrimSpace(workdir))
	}

	resp, err := c.client.Agent.List(ctx, params)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return []AgentInfo{}, nil
	}

	agents := make([]AgentInfo, 0, len(*resp))
	for _, item := range *resp {
		resolvedName := strings.TrimSpace(item.Name)
		if resolvedName == "" {
			continue
		}
		agents = append(agents, AgentInfo{
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

func (c *Client) GetSession(ctx context.Context, sessionID string) (*ocsdk.Session, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("opencode session id is required")
	}

	return c.client.Session.Get(ctx, resolvedSessionID, ocsdk.SessionGetParams{})
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

func (c *Client) GetSessionMessages(ctx context.Context, sessionID string) ([]SessionMessage, error) {
	raw, err := c.getSessionMessages(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	messages := make([]SessionMessage, 0, len(raw))
	for i, msg := range raw {
		c.logger.Debug("session message response",
			slog.String("session_id", sessionID),
			slog.Int("index", i),
			slog.Any("info", msg.Info),
			slog.Any("parts", msg.Parts),
		)
		messages = append(messages, SessionMessage{
			ID:         strings.TrimSpace(msg.Info.ID),
			ProviderID: strings.TrimSpace(msg.Info.ProviderID),
			ModelID:    strings.TrimSpace(msg.Info.ModelID),
			Mode:       strings.TrimSpace(msg.Info.Mode),
			Role:       string(msg.Info.Role),
		})
	}

	return messages, nil
}

func (c *Client) GetSessionLatestAssistantMessage(ctx context.Context, sessionID string) (*SessionMessage, error) {
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
		msg := SessionMessage{
			ID:          strings.TrimSpace(raw[i].Info.ID),
			ProviderID:  strings.TrimSpace(raw[i].Info.ProviderID),
			ModelID:     strings.TrimSpace(raw[i].Info.ModelID),
			Mode:        strings.TrimSpace(raw[i].Info.Mode),
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

func (c *Client) CreateSession(ctx context.Context, request CreateSessionRequest) (*ocsdk.Session, error) {
	params := ocsdk.SessionNewParams{}
	if strings.TrimSpace(request.Workdir) != "" {
		params.Directory = ocsdk.F(strings.TrimSpace(request.Workdir))
	}
	if strings.TrimSpace(request.Title) != "" {
		params.Title = ocsdk.F(strings.TrimSpace(request.Title))
	}

	return c.client.Session.New(ctx, params)
}

func (c *Client) Prompt(ctx context.Context, request PromptRequest) (*PromptHandle, error) {
	resolvedSessionID := strings.TrimSpace(request.SessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("opencode session id is required")
	}
	resolvedContent := strings.TrimSpace(request.Content)
	if resolvedContent == "" {
		return nil, fmt.Errorf("message content is required")
	}

	parts := []ocsdk.SessionPromptParamsPartUnion{
		ocsdk.TextPartInputParam{
			Type: ocsdk.F(ocsdk.TextPartInputTypeText),
			Text: ocsdk.F(resolvedContent),
		},
	}

	params := ocsdk.SessionPromptParams{Parts: ocsdk.F(parts)}
	resolvedWorkdir := strings.TrimSpace(request.Workdir)
	if resolvedWorkdir != "" {
		params.Directory = ocsdk.F(resolvedWorkdir)
	}

	resolvedModel := strings.TrimSpace(request.Model)
	if resolvedModel != "" {
		providerID, modelID, err := c.resolveModel(ctx, resolvedModel, resolvedWorkdir)
		if err != nil {
			return nil, err
		}
		params.Model = ocsdk.F(ocsdk.SessionPromptParamsModel{
			ProviderID: ocsdk.F(providerID),
			ModelID:    ocsdk.F(modelID),
		})
	}

	resolvedAgent := strings.TrimSpace(request.Agent)
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

	return &PromptHandle{done: doneCh, err: errCh}, nil
}

func (c *Client) PollMessagesAfter(ctx context.Context, sessionID string, afterCompletedAt float64) ([]*PromptResult, error) {
	messages, err := c.client.Session.Messages(ctx, sessionID, ocsdk.SessionMessagesParams{})
	if err != nil {
		return nil, err
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
			return nil, fmt.Errorf("prompt failed: %s", assistant.Error.Name)
		}
		if assistant.Time.Completed <= 0 {
			continue
		}
		candidates = append(candidates, assistant)
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Time.Completed < candidates[j].Time.Completed
	})

	results := make([]*PromptResult, 0, len(candidates))
	for _, candidate := range candidates {
		resp, err := c.client.Session.Message(ctx, sessionID, candidate.ID, ocsdk.SessionMessageParams{})
		if err != nil {
			return nil, err
		}
		result, err := c.buildPromptResult(ctx, sessionID, candidate.Time.Completed, resp)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}

	return results, nil
}

func (c *Client) buildPromptResult(ctx context.Context, fallbackSessionID string, completedAt float64, response *ocsdk.SessionMessageResponse) (*PromptResult, error) {
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

	result := &PromptResult{
		Reply:       extractReply(response.Parts),
		SessionID:   resultSessionID,
		ProviderID:  strings.TrimSpace(assistant.ProviderID),
		ModelID:     strings.TrimSpace(assistant.ModelID),
		Mode:        strings.TrimSpace(assistant.Mode),
		CompletedAt: completedAt,
	}

	c.fillPromptResultSessionInfo(ctx, result)

	return result, nil
}

func (c *Client) fillPromptResultSessionInfo(ctx context.Context, result *PromptResult) {
	if result == nil {
		return
	}
	if strings.TrimSpace(result.SessionID) == "" {
		return
	}

	session, err := c.client.Session.Get(ctx, result.SessionID, ocsdk.SessionGetParams{})
	if err != nil || session == nil {
		return
	}

	result.Title = strings.TrimSpace(session.Title)
	if strings.TrimSpace(session.Directory) != "" {
		result.Workdir = strings.TrimSpace(session.Directory)
	}
}

func (c *Client) resolveModel(ctx context.Context, model string, workdir string) (string, string, error) {
	resolvedModel := strings.TrimSpace(model)
	if resolvedModel == "" {
		return "", "", fmt.Errorf("model is required")
	}

	if strings.Contains(resolvedModel, "/") {
		pair := strings.SplitN(resolvedModel, "/", 2)
		providerID := strings.TrimSpace(pair[0])
		modelID := strings.TrimSpace(pair[1])
		if providerID == "" || modelID == "" {
			return "", "", fmt.Errorf("invalid model format: %s", resolvedModel)
		}
		return providerID, modelID, nil
	}

	models, err := c.ListModels(ctx, workdir)
	if err != nil {
		return "", "", err
	}

	matches := make([]ModelInfo, 0, 4)
	for _, candidate := range models {
		if strings.EqualFold(strings.TrimSpace(candidate.ModelID), resolvedModel) {
			matches = append(matches, candidate)
		}
	}

	if len(matches) == 0 {
		return "", "", fmt.Errorf("model not found: %s", resolvedModel)
	}
	if len(matches) > 1 {
		return "", "", fmt.Errorf("ambiguous model %s, use provider/model", resolvedModel)
	}

	return strings.TrimSpace(matches[0].ProviderID), strings.TrimSpace(matches[0].ModelID), nil
}

func extractReply(parts []ocsdk.Part) string {
	builder := strings.Builder{}

	for _, part := range parts {
		if part.Type != ocsdk.PartTypeText {
			continue
		}

		if text := strings.TrimSpace(part.Text); text != "" {
			if builder.Len() > 0 {
				builder.WriteString("\n")
			}
			builder.WriteString(text)
		}
	}

	return strings.TrimSpace(builder.String())
}
