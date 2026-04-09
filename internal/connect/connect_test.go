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
		pollResults: []*opencode.PromptResult{
			{
				Reply:     "hello",
				SessionID: "ses_bound",
				Title:     "Bound Session",
				Workdir:   "/tmp/project",
			},
		},
	}

	connector := New(WithOpencodeClient(client), WithConversationStore(store))
	var resp *Message
	err := connector.Handle(context.Background(), &Message{
		Content: "hello world",
		Chat:    ChatContext{SessionID: "chat-1"},
	}, func(msg *Message) error { resp = msg; return nil })
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
		pollResults: []*opencode.PromptResult{
			{
				Reply:     "hello",
				SessionID: "ses_created",
				Title:     "Created",
				Workdir:   "/repo/created",
			},
		},
	}

	connector := New(WithOpencodeClient(client))
	var resp *Message
	err := connector.Handle(context.Background(), &Message{Content: "hello world", Chat: ChatContext{SessionID: "chat-2"}}, func(msg *Message) error { resp = msg; return nil })
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
	client := &fakeSessionClient{
		getSession:             &opencode.Session{ID: "ses_target", Title: "Target", Directory: "/repo/target"},
		latestAssistantMessage: &opencode.SessionMessage{ID: "msg-1", ProviderID: "openai", ModelID: "gpt-5.4", Mode: "build", Role: "assistant"},
	}
	connector := New(WithOpencodeClient(client), WithConversationStore(store))

	var resp *Message
	err := connector.Handle(context.Background(), &Message{Content: "/session attach ses_target", Chat: ChatContext{SessionID: "chat-1"}}, func(msg *Message) error { resp = msg; return nil })
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if got, want := client.getSessionID, "ses_target"; got != want {
		t.Fatalf("GetSession() session = %q, want %q", got, want)
	}
	if got, want := resp.Opencode.SessionID, "ses_target"; got != want {
		t.Fatalf("response session = %q, want %q", got, want)
	}
	if got, want := resp.Opencode.Workdir, "/repo/target"; got != want {
		t.Fatalf("response workdir = %q, want %q", got, want)
	}
	if got, want := resp.Opencode.Model, "openai/gpt-5.4 (build)"; got != want {
		t.Fatalf("response model = %q, want %q", got, want)
	}

	state, ok := store.Get("chat-1")
	if !ok {
		t.Fatalf("conversation state missing")
	}
	if got, want := state.OpencodeSessionID, "ses_target"; got != want {
		t.Fatalf("bound session = %q, want %q", got, want)
	}
	if got, want := state.DefaultWorkdir, "/repo/target"; got != want {
		t.Fatalf("default workdir = %q, want %q", got, want)
	}
	if got, want := state.LastProviderID, "openai"; got != want {
		t.Fatalf("last provider id = %q, want %q", got, want)
	}
	if got, want := state.LastModelID, "gpt-5.4"; got != want {
		t.Fatalf("last model id = %q, want %q", got, want)
	}
	if got, want := state.LastMode, "build"; got != want {
		t.Fatalf("last mode = %q, want %q", got, want)
	}
}

func TestHandleSessionCurrentCommandFetchesSessionMetadata(t *testing.T) {
	t.Parallel()

	store := NewMemoryConversationStore(0, 0)
	store.PutBinding("chat-1", "ses_target")
	store.SetDefaultModel("chat-1", "openai/gpt-5.4")
	store.SetDefaultWorkdir("chat-1", "/repo/local")
	store.SetLastModelInfo("chat-1", "anthropic", "claude-3.5", "architect")

	client := &fakeSessionClient{
		getSession: &opencode.Session{ID: "ses_target", Title: "Target", Directory: "/repo/remote"},
	}
	connector := New(WithOpencodeClient(client), WithConversationStore(store))

	var resp *Message
	err := connector.Handle(context.Background(), &Message{Content: "/session current", Chat: ChatContext{SessionID: "chat-1"}}, func(msg *Message) error { resp = msg; return nil })
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if got, want := client.getSessionID, "ses_target"; got != want {
		t.Fatalf("GetSession() session = %q, want %q", got, want)
	}
	if got, want := resp.Opencode.SessionID, "ses_target"; got != want {
		t.Fatalf("response session = %q, want %q", got, want)
	}
	if got, want := resp.Opencode.Workdir, "/repo/remote"; got != want {
		t.Fatalf("response workdir = %q, want %q", got, want)
	}
	if got, want := resp.Opencode.Model, "anthropic/claude-3.5 (architect)"; got != want {
		t.Fatalf("response model = %q, want %q", got, want)
	}
}

func TestHandleModelSetCommand(t *testing.T) {
	t.Parallel()

	store := NewMemoryConversationStore(0, 0)
	client := &fakeSessionClient{}
	connector := New(WithOpencodeClient(client), WithConversationStore(store))

	err := connector.Handle(context.Background(), &Message{Content: "/model set openai/gpt-5.4", Chat: ChatContext{SessionID: "chat-1"}}, nil)
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

func TestHandleAgentSetCommand(t *testing.T) {
	t.Parallel()

	store := NewMemoryConversationStore(0, 0)
	client := &fakeSessionClient{}
	connector := New(WithOpencodeClient(client), WithConversationStore(store))

	err := connector.Handle(context.Background(), &Message{Content: "/agent set quick", Chat: ChatContext{SessionID: "chat-1"}}, nil)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	state, ok := store.Get("chat-1")
	if !ok {
		t.Fatalf("conversation state missing")
	}
	if got, want := state.DefaultAgent, "quick"; got != want {
		t.Fatalf("default agent = %q, want %q", got, want)
	}
}

func TestHandleAgentListCommand(t *testing.T) {
	t.Parallel()

	client := &fakeSessionClient{
		listAgents: []opencode.AgentInfo{
			{Name: "build", Mode: "subagent", Description: "Build code"},
			{Name: "quick", Mode: "subagent"},
		},
	}
	connector := New(WithOpencodeClient(client))

	var resp *Message
	err := connector.Handle(context.Background(), &Message{Content: "/agent list"}, func(msg *Message) error { resp = msg; return nil })
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if got, want := resp.Content, "- build (subagent): Build code\n- quick (subagent)"; got != want {
		t.Fatalf("response content = %q, want %q", got, want)
	}
}

func TestHandleNewCommandWithAgentFlag(t *testing.T) {
	t.Parallel()

	store := NewMemoryConversationStore(0, 0)
	client := &fakeSessionClient{createdSession: &opencode.Session{ID: "ses_created", Title: "Created", Directory: "/repo/created"}}
	connector := New(WithOpencodeClient(client), WithConversationStore(store))

	var resp *Message
	err := connector.Handle(context.Background(), &Message{Content: "/new --agent build", Chat: ChatContext{SessionID: "chat-1"}}, func(msg *Message) error { resp = msg; return nil })
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if got, want := resp.Opencode.Agent, "build"; got != want {
		t.Fatalf("response agent = %q, want %q", got, want)
	}
	state, ok := store.Get("chat-1")
	if !ok {
		t.Fatalf("conversation state missing")
	}
	if got, want := state.DefaultAgent, "build"; got != want {
		t.Fatalf("default agent = %q, want %q", got, want)
	}
}

func TestHandleUsesConversationDefaultAgentForPrompt(t *testing.T) {
	t.Parallel()

	store := NewMemoryConversationStore(0, 0)
	store.PutBinding("chat-1", "ses_bound")
	store.SetDefaultAgent("chat-1", "quick")

	client := &fakeSessionClient{pollResults: []*opencode.PromptResult{{Reply: "ok", SessionID: "ses_bound"}}}
	connector := New(WithOpencodeClient(client), WithConversationStore(store))

	var resp *Message
	err := connector.Handle(context.Background(), &Message{Content: "hello", Chat: ChatContext{SessionID: "chat-1"}}, func(msg *Message) error { resp = msg; return nil })
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if got, want := client.promptRequest.Agent, "quick"; got != want {
		t.Fatalf("Prompt() agent = %q, want %q", got, want)
	}
	if got, want := resp.Opencode.Agent, "quick"; got != want {
		t.Fatalf("response agent = %q, want %q", got, want)
	}
}

func TestHandleCommandErrorReturnsMessage(t *testing.T) {
	t.Parallel()

	store := NewMemoryConversationStore(0, 0)
	client := &fakeSessionClient{getErr: fmt.Errorf("connection failed")}
	connector := New(WithOpencodeClient(client), WithConversationStore(store))

	var resp *Message
	err := connector.Handle(context.Background(), &Message{Content: "/session attach ses_missing", Chat: ChatContext{SessionID: "chat-1"}}, func(msg *Message) error { resp = msg; return nil })
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
	err := connector.Handle(context.Background(), &Message{Content: "hello"}, nil)
	if err == nil {
		t.Fatal("Handle() error = nil, want error")
	}
	if got, want := err.Error(), "opencode client is required"; got != want {
		t.Fatalf("Handle() error = %q, want %q", got, want)
	}
}

type fakeSessionClient struct {
	pollResults            []*opencode.PromptResult
	createdSession         *opencode.Session
	listSessions           []opencode.Session
	listModels             []opencode.ModelInfo
	listAgents             []opencode.AgentInfo
	getSession             *opencode.Session
	getSessionMessages     []opencode.SessionMessage
	latestAssistantMessage *opencode.SessionMessage
	getErr                 error
	createErr              error
	promptErr              error
	pollErr                error
	getSessionID           string
	promptRequest          opencode.PromptRequest
	createRequest          opencode.CreateSessionRequest
}

func (f *fakeSessionClient) ListSessions(context.Context, string) ([]opencode.Session, error) {
	return f.listSessions, nil
}

func (f *fakeSessionClient) ListModels(context.Context, string) ([]opencode.ModelInfo, error) {
	return f.listModels, nil
}

func (f *fakeSessionClient) ListAgents(context.Context, string) ([]opencode.AgentInfo, error) {
	return f.listAgents, nil
}

func (f *fakeSessionClient) GetSession(_ context.Context, sessionID string) (*opencode.Session, error) {
	f.getSessionID = sessionID
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getSession != nil {
		return f.getSession, nil
	}
	return &opencode.Session{ID: sessionID}, nil
}

func (f *fakeSessionClient) GetSessionMessages(context.Context, string) ([]opencode.SessionMessage, error) {
	if f.getSessionMessages != nil {
		return f.getSessionMessages, nil
	}
	return []opencode.SessionMessage{}, nil
}

func (f *fakeSessionClient) GetSessionLatestAssistantMessage(context.Context, string) (*opencode.SessionMessage, error) {
	return f.latestAssistantMessage, nil
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

func (f *fakeSessionClient) Prompt(_ context.Context, request opencode.PromptRequest) (*opencode.PromptHandle, error) {
	f.promptRequest = request
	if f.promptErr != nil {
		return nil, f.promptErr
	}
	doneCh := make(chan struct{})
	close(doneCh)
	return opencode.NewPromptHandle(doneCh, nil), nil
}

func (f *fakeSessionClient) PollCompletedMessages(_ context.Context, _ string, _ float64, _ float64) ([]*opencode.PromptResult, error) {
	if f.pollErr != nil {
		return nil, f.pollErr
	}
	results := f.pollResults
	f.pollResults = nil // consume on first poll so the loop exits
	if len(results) == 0 {
		return nil, nil
	}
	return results, nil
}
