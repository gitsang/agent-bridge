package model_cache

import (
	"context"
	"errors"
	"testing"

	"github.com/gitsang/agent-bridge/internal/types"
)

func TestCacheHumanizeZeroRef(t *testing.T) {
	client := &fakeClient{}
	cache := New()

	if got := cache.Humanize(context.Background(), types.ModelRef{}, client, "/tmp/project"); got != "" {
		t.Fatalf("Humanize() = %q, want empty string", got)
	}
	if got := client.listModelsCalls; got != 0 {
		t.Fatalf("ListModels() calls = %d, want 0", got)
	}
}

func TestCacheHumanizeRefreshesOnMiss(t *testing.T) {
	ref := types.ModelRef{ProviderID: "openai", ModelID: "gpt-4.1"}
	client := &fakeClient{
		models: []types.ModelInfo{{
			ModelRef:     ref,
			ProviderName: "OpenAI",
			ModelName:    "GPT-4.1",
		}},
	}
	cache := New()

	got := cache.Humanize(context.Background(), ref, client, "/tmp/project")

	if got != "OpenAI/GPT-4.1" {
		t.Fatalf("Humanize() = %q, want %q", got, "OpenAI/GPT-4.1")
	}
	if got := client.listModelsCalls; got != 1 {
		t.Fatalf("ListModels() calls = %d, want 1", got)
	}
}

func TestCacheHumanizeUsesCachedEntry(t *testing.T) {
	ref := types.ModelRef{ProviderID: "openai", ModelID: "gpt-4.1"}
	client := &fakeClient{
		models: []types.ModelInfo{{
			ModelRef:     ref,
			ProviderName: "OpenAI",
			ModelName:    "GPT-4.1",
		}},
	}
	cache := New()

	cache.Humanize(context.Background(), ref, client, "/tmp/project")
	got := cache.Humanize(context.Background(), ref, client, "/tmp/project")

	if got != "OpenAI/GPT-4.1" {
		t.Fatalf("Humanize() = %q, want %q", got, "OpenAI/GPT-4.1")
	}
	if got := client.listModelsCalls; got != 1 {
		t.Fatalf("ListModels() calls = %d, want 1", got)
	}
}

func TestCacheHumanizeFallsBackToRefWhenRefreshFails(t *testing.T) {
	ref := types.ModelRef{ProviderID: "anthropic", ModelID: "claude-sonnet-4"}
	client := &fakeClient{listModelsErr: errors.New("boom")}
	cache := New()

	got := cache.Humanize(context.Background(), ref, client, "/tmp/project")

	if got != ref.String() {
		t.Fatalf("Humanize() = %q, want %q", got, ref.String())
	}
	if got := client.listModelsCalls; got != 1 {
		t.Fatalf("ListModels() calls = %d, want 1", got)
	}
}

type fakeClient struct {
	models          []types.ModelInfo
	listModelsErr   error
	listModelsCalls int
}

func (c *fakeClient) ListModels(context.Context, string) ([]types.ModelInfo, error) {
	c.listModelsCalls++
	if c.listModelsErr != nil {
		return nil, c.listModelsErr
	}
	return c.models, nil
}

func (c *fakeClient) ResolveModel(context.Context, string, string) (types.ModelRef, error) {
	return types.ModelRef{}, nil
}

func (c *fakeClient) ListAgents(context.Context, string) ([]types.AgentInfo, error) {
	return nil, nil
}

func (c *fakeClient) ListSessions(context.Context, string) ([]types.Session, error) {
	return nil, nil
}

func (c *fakeClient) ListAllSessions(context.Context) ([]types.Session, error) {
	return nil, nil
}

func (c *fakeClient) GetSession(context.Context, string) (*types.Session, error) {
	return nil, nil
}

func (c *fakeClient) CreateSession(context.Context, types.CreateSessionRequest) (*types.Session, error) {
	return nil, nil
}

func (c *fakeClient) GetMessages(context.Context, string) ([]types.Message, error) {
	return nil, nil
}

func (c *fakeClient) GetLatestAssistantMessage(context.Context, string) (*types.Message, error) {
	return nil, nil
}

func (c *fakeClient) Prompt(context.Context, string, string, ...types.PromptOptionFunc) (*types.PromptHandle, error) {
	return nil, nil
}

func (c *fakeClient) PollMessagesAfter(context.Context, string, float64, types.MessageOutputOptions) ([]*types.Message, error) {
	return nil, nil
}

func (c *fakeClient) ListPendingPermissions(context.Context, string) ([]types.PermissionRequest, error) {
	return nil, nil
}

func (c *fakeClient) ReplyPermission(context.Context, string, string, types.PermissionReply) error {
	return nil
}

func (c *fakeClient) ListPendingQuestions(context.Context, string) ([]types.QuestionRequest, error) {
	return nil, nil
}

func (c *fakeClient) ReplyQuestion(context.Context, string, string, [][]string) error {
	return nil
}

func (c *fakeClient) RejectQuestion(context.Context, string, string) error {
	return nil
}
