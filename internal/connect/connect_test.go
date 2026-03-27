package connect

import (
	"context"
	"fmt"
	"testing"

	"github.com/gitsang/opencode-connect/internal/opencode"
)

func TestHandleUsesConversationBindingForPrompt(t *testing.T) {
	t.Parallel()

	store := NewMemoryConversationStore(0, 0)
	store.PutBinding("chat-1", "ses_bound")

	client := &fakeSessionClient{
		promptResult: &opencode.PromptResult{
			Reply:     "hello",
			SessionID: "ses_bound",
			Title:     "Bound Session",
			Model:     "openai/gpt-5.4",
			Workdir:   "/tmp/project",
		},
	}

	connector := New(WithOpencodeClient(client), WithConversationStore(store))
	resp, err := connector.Handle(context.Background(), &Message{
		Content: "hello world",
		Chat:    ChatContext{SessionID: "chat-1"},
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if got, want := client.promptRequest.SessionID, "ses_bound"; got != want {
		t.Fatalf("Prompt() session = %q, want %q", got, want)
	}
	if got, want := resp.Opencode.SessionID, "ses_bound"; got != want {
		t.Fatalf("response session = %q, want %q", got, want)
	}
}

func TestHandleCreatesSessionWhenNoBinding(t *testing.T) {
	t.Parallel()

	client := &fakeSessionClient{
		createdSession: &opencode.Session{ID: "ses_created", Title: "Created", Directory: "/repo/created"},
		promptResult: &opencode.PromptResult{
			Reply:     "hello",
			SessionID: "ses_created",
			Title:     "Created",
			Model:     "openai/gpt-5.4",
			Workdir:   "/repo/created",
		},
	}

	connector := New(WithOpencodeClient(client))
	resp, err := connector.Handle(context.Background(), &Message{Content: "hello world", Chat: ChatContext{SessionID: "chat-2"}})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if got, want := client.createRequest.Workdir, ""; got != want {
		t.Fatalf("CreateSession() workdir = %q, want %q", got, want)
	}
	if got, want := resp.Opencode.SessionID, "ses_created"; got != want {
		t.Fatalf("response session = %q, want %q", got, want)
	}
}

func TestHandleSessionAttachCommand(t *testing.T) {
	t.Parallel()

	store := NewMemoryConversationStore(0, 0)
	client := &fakeSessionClient{}
	connector := New(WithOpencodeClient(client), WithConversationStore(store))

	resp, err := connector.Handle(context.Background(), &Message{Content: "/session attach ses_target", Chat: ChatContext{SessionID: "chat-1"}})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if got, want := client.getSessionID, "ses_target"; got != want {
		t.Fatalf("GetSession() session = %q, want %q", got, want)
	}
	if got, want := resp.Opencode.SessionID, "ses_target"; got != want {
		t.Fatalf("response session = %q, want %q", got, want)
	}

	state, ok := store.Get("chat-1")
	if !ok {
		t.Fatalf("conversation state missing")
	}
	if got, want := state.OpencodeSessionID, "ses_target"; got != want {
		t.Fatalf("bound session = %q, want %q", got, want)
	}
}

func TestHandleModelSetCommand(t *testing.T) {
	t.Parallel()

	store := NewMemoryConversationStore(0, 0)
	client := &fakeSessionClient{}
	connector := New(WithOpencodeClient(client), WithConversationStore(store))

	_, err := connector.Handle(context.Background(), &Message{Content: "/model set openai/gpt-5.4", Chat: ChatContext{SessionID: "chat-1"}})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	state, ok := store.Get("chat-1")
	if !ok {
		t.Fatalf("conversation state missing")
	}
	if got, want := state.DefaultModel, "openai/gpt-5.4"; got != want {
		t.Fatalf("default model = %q, want %q", got, want)
	}
}

func TestHandleCommandErrorReturnsMessage(t *testing.T) {
	t.Parallel()

	store := NewMemoryConversationStore(0, 0)
	client := &fakeSessionClient{getErr: fmt.Errorf("connection failed")}
	connector := New(WithOpencodeClient(client), WithConversationStore(store))

	resp, err := connector.Handle(context.Background(), &Message{Content: "/session attach ses_missing", Chat: ChatContext{SessionID: "chat-1"}})
	if err != nil {
		t.Fatalf("Handle() should not return error, got: %v", err)
	}
	if got, want := resp.Content, "Error: session not found: ses_missing"; got != want {
		t.Fatalf("error message = %q, want %q", got, want)
	}
}

func TestHandleRequiresOpencodeClient(t *testing.T) {
	t.Parallel()

	connector := New()
	_, err := connector.Handle(context.Background(), &Message{Content: "hello"})
	if err == nil {
		t.Fatal("Handle() error = nil, want error")
	}
	if got, want := err.Error(), "opencode client is required"; got != want {
		t.Fatalf("Handle() error = %q, want %q", got, want)
	}
}

type fakeSessionClient struct {
	promptResult   *opencode.PromptResult
	createdSession *opencode.Session
	listSessions   []opencode.Session
	listModels     []opencode.ModelInfo
	getErr         error
	createErr      error
	promptErr      error
	getSessionID   string
	promptRequest  opencode.PromptRequest
	createRequest  opencode.CreateSessionRequest
}

func (f *fakeSessionClient) ListSessions(context.Context, string) ([]opencode.Session, error) {
	return f.listSessions, nil
}

func (f *fakeSessionClient) ListModels(context.Context, string) ([]opencode.ModelInfo, error) {
	return f.listModels, nil
}

func (f *fakeSessionClient) GetSession(_ context.Context, sessionID string) (*opencode.Session, error) {
	f.getSessionID = sessionID
	if f.getErr != nil {
		return nil, f.getErr
	}
	return &opencode.Session{ID: sessionID}, nil
}

func (f *fakeSessionClient) CreateSession(_ context.Context, request opencode.CreateSessionRequest) (*opencode.Session, error) {
	f.createRequest = request
	if f.createErr != nil {
		return nil, f.createErr
	}
	if f.createdSession == nil {
		return nil, fmt.Errorf("created session is required")
	}
	return f.createdSession, nil
}

func (f *fakeSessionClient) Prompt(_ context.Context, request opencode.PromptRequest) (*opencode.PromptResult, error) {
	f.promptRequest = request
	if f.promptErr != nil {
		return nil, f.promptErr
	}
	if f.promptResult == nil {
		return nil, fmt.Errorf("prompt result is required")
	}
	return f.promptResult, nil
}
