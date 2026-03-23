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

	connector := New(WithOpencodeClient(client))
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

	connector := New(WithOpencodeClient(client))
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

func TestHandleCreatesSessionWhenRequestSessionIDMissing(t *testing.T) {
	t.Parallel()

	client := &fakeSessionClient{
		createdSession: &opencode.Session{ID: "ses_created"},
		promptResult: &opencode.PromptResult{
			Reply:             "hello",
			OpencodeSessionID: "ses_created",
		},
	}

	connector := New(WithOpencodeClient(client))
	resp, err := connector.Handle(context.Background(), &Message{
		Message: "hello world",
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if client.createSessionID != "chat" {
		t.Fatalf("CreateSession() input = %q, want %q", client.createSessionID, "chat")
	}
	if client.promptSessionID != "ses_created" {
		t.Fatalf("Prompt() session = %q, want %q", client.promptSessionID, "ses_created")
	}
	if resp.SessionID != "ses_created" {
		t.Fatalf("Handle() session = %q, want %q", resp.SessionID, "ses_created")
	}
}

func TestNewAppliesOptions(t *testing.T) {
	t.Parallel()

	client := &fakeSessionClient{}
	connector := New(WithOpencodeClient(client))
	if connector.opencodeClient != client {
		t.Fatalf("New() opencodeClient = %v, want %v", connector.opencodeClient, client)
	}
}

func TestHandleRequiresOpencodeClient(t *testing.T) {
	t.Parallel()

	connector := New()
	_, err := connector.Handle(context.Background(), &Message{
		SessionID: "session-1",
		Message:   "hello world",
	})
	if err == nil {
		t.Fatal("Handle() error = nil, want error")
	}
	if got, want := err.Error(), "opencode client is required"; got != want {
		t.Fatalf("Handle() error = %q, want %q", got, want)
	}
}

type fakeSessionClient struct {
	promptResult    *opencode.PromptResult
	createdSession  *opencode.Session
	listSessions    []opencode.Session
	getErr          error
	createErr       error
	promptErr       error
	getSessionID    string
	createSessionID string
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

func (f *fakeSessionClient) CreateSession(_ context.Context, sessionID string) (*opencode.Session, error) {
	f.createSessionID = sessionID
	if f.createErr != nil {
		return nil, f.createErr
	}
	if f.createdSession == nil {
		return nil, fmt.Errorf("created session is required")
	}
	return f.createdSession, nil
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
