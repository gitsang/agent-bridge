package opencode

import (
	"context"
	"encoding/base64"
	"fmt"
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

type PromptResult struct {
	Reply             string
	OpencodeSessionID string
	Title             string
	Workdir           string
	Model             string
}

type Client struct {
	client  *ocsdk.Client
	timeout time.Duration
}

func NewClient(baseURL string, options ...Option) *Client {
	resolved := Options{
		Timeout: 10 * time.Minute,
	}

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

	return &Client{
		client:  sdkClient,
		timeout: timeout,
	}
}

func (c *Client) ListSessions(ctx context.Context) ([]ocsdk.Session, error) {
	resp, err := c.client.Session.List(ctx, ocsdk.SessionListParams{})
	if err != nil {
		return nil, err
	}

	if resp == nil {
		return []ocsdk.Session{}, nil
	}

	return *resp, nil
}

func (c *Client) GetSession(ctx context.Context, sessionId string) (*ocsdk.Session, error) {
	if strings.TrimSpace(sessionId) == "" {
		return nil, fmt.Errorf("opencode session id is required")
	}

	return c.client.Session.Get(ctx, sessionId, ocsdk.SessionGetParams{})
}

func (c *Client) CreateSession(ctx context.Context) (*ocsdk.Session, error) {
	return c.client.Session.New(ctx, ocsdk.SessionNewParams{})
}

func (c *Client) Prompt(ctx context.Context, sessionId string, message string) (*PromptResult, error) {
	if sessionId == "" {
		return nil, fmt.Errorf("opencode session id is required")
	}
	if strings.TrimSpace(message) == "" {
		return nil, fmt.Errorf("message is required")
	}

	promptCtx, promptCancel := context.WithTimeout(ctx, c.timeout)
	defer promptCancel()

	parts := []ocsdk.SessionPromptParamsPartUnion{
		ocsdk.TextPartInputParam{
			Type: ocsdk.F(ocsdk.TextPartInputTypeText),
			Text: ocsdk.F(message),
		},
	}

	params := ocsdk.SessionPromptParams{
		Parts: ocsdk.F(parts),
	}

	resp, err := c.client.Session.Prompt(promptCtx, sessionId, params)
	if err != nil {
		return nil, err
	}

	resolvedSessionID := strings.TrimSpace(resp.Info.SessionID)
	if resolvedSessionID == "" {
		resolvedSessionID = strings.TrimSpace(sessionId)
	}

	result := &PromptResult{
		Reply:             extractReply(resp.Parts),
		OpencodeSessionID: resolvedSessionID,
		Model:             strings.TrimSpace(resp.Info.ModelID),
	}

	if resolvedSessionID == "" {
		return result, nil
	}

	session, err := c.client.Session.Get(promptCtx, resolvedSessionID, ocsdk.SessionGetParams{})
	if err != nil || session == nil {
		return result, nil
	}

	result.Title = strings.TrimSpace(session.Title)
	result.Workdir = strings.TrimSpace(session.Directory)

	return result, nil
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
