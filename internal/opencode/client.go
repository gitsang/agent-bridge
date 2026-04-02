package opencode

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"time"

	ocsdk "github.com/sst/opencode-sdk-go"
	"github.com/sst/opencode-sdk-go/option"
)

type Session = ocsdk.Session

type Option func(*Options)

type Options struct {
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
	OnResult  func(result *PromptResult)
}

type CreateSessionRequest struct {
	Title   string
	Workdir string
}

type PromptResult struct {
	Reply      string
	SessionID  string
	Title      string
	Workdir    string
	Model      string
	ProviderID string
	ModelID    string
	Mode       string
}

type SessionMessage struct {
	ID         string
	ProviderID string
	ModelID    string
	Mode       string
	Role       string
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
	client  *ocsdk.Client
	timeout time.Duration
}

const promptPollInterval = 2 * time.Second

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
	return &Client{client: sdkClient, timeout: timeout}
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

func (c *Client) GetSessionMessages(ctx context.Context, sessionID string) ([]SessionMessage, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("opencode session id is required")
	}

	resp, err := c.client.Session.Messages(ctx, resolvedSessionID, ocsdk.SessionMessagesParams{})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return []SessionMessage{}, nil
	}

	messages := make([]SessionMessage, 0, len(*resp))
	for _, msg := range *resp {
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

func (c *Client) Prompt(ctx context.Context, request PromptRequest) (*PromptResult, error) {
	resolvedSessionID := strings.TrimSpace(request.SessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("opencode session id is required")
	}
	resolvedContent := strings.TrimSpace(request.Content)
	if resolvedContent == "" {
		return nil, fmt.Errorf("message content is required")
	}

	promptCtx := ctx
	var promptCancel context.CancelFunc = func() {}
	if c.timeout > 0 {
		promptCtx, promptCancel = context.WithTimeout(ctx, c.timeout)
	}
	defer promptCancel()

	promptStart := float64(time.Now().UnixMilli()) / 1000

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
		providerID, modelID, err := c.resolveModel(promptCtx, resolvedModel, resolvedWorkdir)
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

	responseChan := make(chan *ocsdk.SessionPromptResponse, 1)
	errorChan := make(chan error, 1)

	go func() {
		resp, err := c.client.Session.Prompt(promptCtx, resolvedSessionID, params)
		if err != nil {
			errorChan <- err
			return
		}
		responseChan <- resp
	}()

	ticker := time.NewTicker(promptPollInterval)
	defer ticker.Stop()

	var lastReportedAt float64
	var lastResult *PromptResult

	for {
		select {
		case <-promptCtx.Done():
			if lastResult != nil {
				return lastResult, nil
			}
			return nil, promptCtx.Err()
		case err := <-errorChan:
			return nil, err
		case resp := <-responseChan:
			result, err := c.buildPromptResultFromPromptResponse(promptCtx, resolvedSessionID, resolvedWorkdir, resp)
			if err != nil {
				return nil, err
			}
			if request.OnResult != nil {
				request.OnResult(result)
			}
			return result, nil
		case <-ticker.C:
			newResults, err := c.pollNewCompletedMessages(promptCtx, resolvedSessionID, promptStart, lastReportedAt)
			if err != nil {
				return nil, err
			}
			for _, messageResp := range newResults {
				result, err := c.buildPromptResultFromMessageResponse(promptCtx, resolvedSessionID, resolvedWorkdir, messageResp)
				if err != nil {
					return nil, err
				}
				lastResult = result
				lastReportedAt = messageResp.Info.AsUnion().(ocsdk.AssistantMessage).Time.Completed
				if request.OnResult != nil {
					request.OnResult(result)
				}
			}
			if lastResult != nil && request.OnResult == nil {
				return lastResult, nil
			}
		}
	}
}

func (c *Client) pollNewCompletedMessages(ctx context.Context, sessionID string, promptStart float64, lastReportedAt float64) ([]*ocsdk.SessionMessageResponse, error) {
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
		if assistant.Time.Created+0.001 < promptStart {
			continue
		}
		if assistant.Time.Completed <= lastReportedAt {
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

	results := make([]*ocsdk.SessionMessageResponse, 0, len(candidates))
	for _, candidate := range candidates {
		resp, err := c.client.Session.Message(ctx, sessionID, candidate.ID, ocsdk.SessionMessageParams{})
		if err != nil {
			return nil, err
		}
		results = append(results, resp)
	}

	return results, nil
}

func (c *Client) buildPromptResultFromPromptResponse(ctx context.Context, fallbackSessionID string, fallbackWorkdir string, response *ocsdk.SessionPromptResponse) (*PromptResult, error) {
	if response == nil {
		return nil, fmt.Errorf("empty prompt response")
	}
	if response.Info.Error.Name != "" {
		return nil, fmt.Errorf("prompt failed: %s", response.Info.Error.Name)
	}

	resultSessionID := strings.TrimSpace(response.Info.SessionID)
	if resultSessionID == "" {
		resultSessionID = strings.TrimSpace(fallbackSessionID)
	}

	result := &PromptResult{
		Reply:      extractReply(response.Parts),
		SessionID:  resultSessionID,
		Model:      strings.TrimSpace(response.Info.ModelID),
		Workdir:    strings.TrimSpace(fallbackWorkdir),
		ProviderID: strings.TrimSpace(response.Info.ProviderID),
		ModelID:    strings.TrimSpace(response.Info.ModelID),
		Mode:       strings.TrimSpace(response.Info.Mode),
	}

	c.fillPromptResultSessionInfo(ctx, result)

	return result, nil
}

func (c *Client) buildPromptResultFromMessageResponse(ctx context.Context, fallbackSessionID string, fallbackWorkdir string, response *ocsdk.SessionMessageResponse) (*PromptResult, error) {
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
		Reply:      extractReply(response.Parts),
		SessionID:  resultSessionID,
		Model:      strings.TrimSpace(assistant.ModelID),
		Workdir:    strings.TrimSpace(fallbackWorkdir),
		ProviderID: strings.TrimSpace(assistant.ProviderID),
		ModelID:    strings.TrimSpace(assistant.ModelID),
		Mode:       strings.TrimSpace(assistant.Mode),
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
