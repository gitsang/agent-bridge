package connect

import (
	"context"
	"fmt"
	"testing"

	"github.com/gitsang/opencode-connect/internal/opencode"
)

func TestHandleUsesRequestSessionID(t *testing.T) {
	t.Parallel()

	client := &fakeSessionClient{
		promptResult: &opencode.PromptResult{
			Reply:             "hello",
			OpencodeSessionID: "opencode-session-1",
		},
	}

	connector := New(client)
	resp, err := connector.Handle(context.Background(), &Message{
		SessionID: "opencode-session-1",
		Message:   "hello world",
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp.SessionID != "opencode-session-1" {
		t.Fatalf("Handle() session = %q, want %q", resp.SessionID, "opencode-session-1")
	}
	if resp.Message != "hello" {
		t.Fatalf("Handle() message = %q, want %q", resp.Message, "hello")
	}
	if client.promptSessionID != "opencode-session-1" {
		t.Fatalf("Prompt() session = %q, want %q", client.promptSessionID, "opencode-session-1")
	}
}

func TestHandleUsesDirectiveSession(t *testing.T) {
	t.Parallel()

	client := &fakeSessionClient{
		promptResult: &opencode.PromptResult{
			Reply:             "hello",
			OpencodeSessionID: "existing-session",
		},
	}

	connector := New(client)
	resp, err := connector.Handle(context.Background(), &Message{
		SessionID: "ignored-by-directive",
		Message:   "@session:existing-session\n\nhello world",
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp.SessionID != "existing-session" {
		t.Fatalf("Handle() session = %q, want %q", resp.SessionID, "existing-session")
	}
	if resp.Message != "hello" {
		t.Fatalf("Handle() message = %q, want %q", resp.Message, "hello")
	}
	if client.getSessionID != "existing-session" {
		t.Fatalf("GetSession() session = %q, want %q", client.getSessionID, "existing-session")
	}
	if client.promptSessionID != "existing-session" {
		t.Fatalf("Prompt() session = %q, want %q", client.promptSessionID, "existing-session")
	}
}

type fakeSessionClient struct {
	promptResult    *opencode.PromptResult
	listSessions    []opencode.Session
	getErr          error
	promptErr       error
	getSessionID    string
	promptSessionID string
}

func (f *fakeSessionClient) ListSessions(context.Context) ([]opencode.Session, error) {
	return f.listSessions, nil
}

func (f *fakeSessionClient) GetSession(_ context.Context, sessionID string) (*opencode.Session, error) {
	f.getSessionID = sessionID
	if f.getErr != nil {
		return nil, f.getErr
	}
	return &opencode.Session{ID: sessionID}, nil
}

func (f *fakeSessionClient) Prompt(_ context.Context, sessionID string, _ string) (*opencode.PromptResult, error) {
	f.promptSessionID = sessionID
	if f.promptErr != nil {
		return nil, f.promptErr
	}
	if f.promptResult == nil {
		return nil, fmt.Errorf("prompt result is required")
	}
	return f.promptResult, nil
}
