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
	Workdir   string
}

type CreateSessionRequest struct {
	Title   string
	Workdir string
}

type PromptResult struct {
	Reply     string
	SessionID string
	Title     string
	Workdir   string
	Model     string
}

type ModelInfo struct {
	ProviderID string
	ModelID    string
	Name       string
}

type Client struct {
	client  *ocsdk.Client
	timeout time.Duration
}

func WithAuthentication(username, password string) Option {
	return func(target *Options) {
		target.Username = username
		target.Password = password
	}
}

func WithTimeout(timeout time.Duration) Option {
	return func(target *Options) {
		if timeout > 0 {
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
	if timeout <= 0 {
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

func (c *Client) GetSession(ctx context.Context, sessionID string) (*ocsdk.Session, error) {
	resolvedSessionID := strings.TrimSpace(sessionID)
	if resolvedSessionID == "" {
		return nil, fmt.Errorf("opencode session id is required")
	}

	return c.client.Session.Get(ctx, resolvedSessionID, ocsdk.SessionGetParams{})
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

	promptCtx, promptCancel := context.WithTimeout(ctx, c.timeout)
	defer promptCancel()

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

	resp, err := c.client.Session.Prompt(promptCtx, resolvedSessionID, params)
	if err != nil {
		return nil, err
	}

	resultSessionID := strings.TrimSpace(resp.Info.SessionID)
	if resultSessionID == "" {
		resultSessionID = resolvedSessionID
	}

	result := &PromptResult{
		Reply:     extractReply(resp.Parts),
		SessionID: resultSessionID,
		Model:     strings.TrimSpace(resp.Info.ModelID),
		Workdir:   resolvedWorkdir,
	}

	if resultSessionID == "" {
		return result, nil
	}

	session, err := c.client.Session.Get(promptCtx, resultSessionID, ocsdk.SessionGetParams{})
	if err != nil || session == nil {
		return result, nil
	}

	result.Title = strings.TrimSpace(session.Title)
	if strings.TrimSpace(session.Directory) != "" {
		result.Workdir = strings.TrimSpace(session.Directory)
	}

	return result, nil
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
