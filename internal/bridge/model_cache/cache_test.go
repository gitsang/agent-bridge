package model_cache

import (
	"context"
	"errors"
	"testing"

	"github.com/gitsang/agent-bridge/internal/agent"
)

func TestCacheHumanizeZeroRef(t *testing.T) {
	client := &fakeClient{}
	cache := New()

	if got := cache.Humanize(context.Background(), agent.ModelRef{}, client, "/tmp/project"); got != "" {
		t.Fatalf("Humanize() = %q, want empty string", got)
	}
	if got := client.listModelsCalls; got != 0 {
		t.Fatalf("ListModels() calls = %d, want 0", got)
	}
}

func TestCacheHumanizeRefreshesOnMiss(t *testing.T) {
	ref := agent.ModelRef{ProviderID: "openai", ModelID: "gpt-4.1"}
	client := &fakeClient{
		models: []agent.ModelInfo{{
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
	ref := agent.ModelRef{ProviderID: "openai", ModelID: "gpt-4.1"}
	client := &fakeClient{
		models: []agent.ModelInfo{{
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
	ref := agent.ModelRef{ProviderID: "anthropic", ModelID: "claude-sonnet-4"}
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
	models          []agent.ModelInfo
	listModelsErr   error
	listModelsCalls int
}

func (c *fakeClient) ListModels(context.Context, string) ([]agent.ModelInfo, error) {
	c.listModelsCalls++
	if c.listModelsErr != nil {
		return nil, c.listModelsErr
	}
	return c.models, nil
}

func (c *fakeClient) ResolveModel(context.Context, string, string) (agent.ModelRef, error) {
	return agent.ModelRef{}, nil
}

func (c *fakeClient) ListAgents(context.Context, string) ([]agent.AgentInfo, error) {
	return nil, nil
}

func (c *fakeClient) ListSessions(context.Context, string) ([]agent.Session, error) {
	return nil, nil
}

func (c *fakeClient) ListAllSessions(context.Context) ([]agent.Session, error) {
	return nil, nil
}

func (c *fakeClient) GetSession(context.Context, string) (*agent.Session, error) {
	return nil, nil
}

func (c *fakeClient) CreateSession(context.Context, agent.CreateSessionRequest) (*agent.Session, error) {
	return nil, nil
}

func (c *fakeClient) GetMessages(context.Context, string) ([]agent.Message, error) {
	return nil, nil
}

func (c *fakeClient) GetLatestAssistantMessage(context.Context, string) (*agent.Message, error) {
	return nil, nil
}

func (c *fakeClient) Prompt(context.Context, string, string, ...agent.PromptOptionFunc) (*agent.PromptHandle, error) {
	return nil, nil
}

func (c *fakeClient) PollMessagesAfter(context.Context, string, float64, agent.MessageOutputOptions) ([]*agent.Message, error) {
	return nil, nil
}

func (c *fakeClient) ListPendingPermissions(context.Context, string) ([]agent.PermissionRequest, error) {
	return nil, nil
}

func (c *fakeClient) ReplyPermission(context.Context, string, string, agent.PermissionReply) error {
	return nil
}

func (c *fakeClient) ListPendingQuestions(context.Context, string) ([]agent.QuestionRequest, error) {
	return nil, nil
}

func (c *fakeClient) ReplyQuestion(context.Context, string, string, [][]string) error {
	return nil
}

func (c *fakeClient) RejectQuestion(context.Context, string, string) error {
	return nil
}
