package bridge

import (
	"context"
	"testing"

	"github.com/gitsang/agent-bridge/internal/agent"
)

func TestHandlePromptPassesMessageOutputOptions(t *testing.T) {
	doneCh := make(chan struct{})
	close(doneCh)
	errCh := make(chan error)
	client := &fakeAgentClient{
		promptHandle: agent.NewPromptHandle(doneCh, errCh),
		pollMessages: []*agent.Message{{
			SessionID:   "agent-session",
			Content:     "hello",
			CompletedAt: 1,
		}},
	}
	output := agent.MessageOutputOptions{
		Include: []agent.MessageContentKind{agent.MessageContentAnswer},
	}
	bridge := New(
		WithAgentClient(client),
		WithMessageOutputOptions(output),
	)

	var replies []*Message
	err := bridge.Handle(context.Background(), &Message{Content: "hi", Chat: ChatContext{SessionID: "chat-session"}}, func(msg *Message) error {
		replies = append(replies, msg)
		return nil
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if got, want := len(replies), 1; got != want {
		t.Fatalf("reply count = %d, want %d", got, want)
	}
	if got, want := client.pollOutput.Include[0], agent.MessageContentAnswer; got != want {
		t.Fatalf("PollMessagesAfter() include[0] = %q, want %q", got, want)
	}
}

func TestAdvanceCompletedCursorKeepsNewestCompletedResult(t *testing.T) {
	after := float64(5)

	after = advanceCompletedCursor(after, &agent.Message{CompletedAt: 0})
	if got, want := after, float64(5); got != want {
		t.Fatalf("advanceCompletedCursor() with unfinished result = %v, want %v", got, want)
	}

	after = advanceCompletedCursor(after, &agent.Message{CompletedAt: 4})
	if got, want := after, float64(5); got != want {
		t.Fatalf("advanceCompletedCursor() with older result = %v, want %v", got, want)
	}

	after = advanceCompletedCursor(after, &agent.Message{CompletedAt: 7})
	if got, want := after, float64(7); got != want {
		t.Fatalf("advanceCompletedCursor() with newer result = %v, want %v", got, want)
	}
}

type fakeAgentClient struct {
	promptHandle *agent.PromptHandle
	pollMessages []*agent.Message
	pollOutput   agent.MessageOutputOptions
}

func (c *fakeAgentClient) ListModels(context.Context, string) ([]agent.ModelInfo, error) {
	return nil, nil
}

func (c *fakeAgentClient) ResolveModel(context.Context, string, string) (agent.ModelRef, error) {
	return agent.ModelRef{}, nil
}

func (c *fakeAgentClient) ListAgents(context.Context, string) ([]agent.AgentInfo, error) {
	return nil, nil
}

func (c *fakeAgentClient) ListSessions(context.Context, string) ([]agent.Session, error) {
	return nil, nil
}

func (c *fakeAgentClient) GetSession(context.Context, string) (*agent.Session, error) {
	return nil, nil
}

func (c *fakeAgentClient) CreateSession(context.Context, agent.CreateSessionRequest) (*agent.Session, error) {
	return &agent.Session{ID: "agent-session"}, nil
}

func (c *fakeAgentClient) GetMessages(context.Context, string) ([]agent.Message, error) {
	return nil, nil
}

func (c *fakeAgentClient) GetLatestAssistantMessage(context.Context, string) (*agent.Message, error) {
	return nil, nil
}

func (c *fakeAgentClient) Prompt(context.Context, string, string, ...agent.PromptOptionFunc) (*agent.PromptHandle, error) {
	return c.promptHandle, nil
}

func (c *fakeAgentClient) PollMessagesAfter(_ context.Context, _ string, _ float64, output agent.MessageOutputOptions) ([]*agent.Message, error) {
	c.pollOutput = output
	return c.pollMessages, nil
}
