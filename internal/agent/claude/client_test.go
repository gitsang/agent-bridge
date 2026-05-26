package claude

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gitsang/agent-bridge/internal/agent"
)

func TestClientPromptAggregatesStreamJSONAndResumesSession(t *testing.T) {
	var requests []ProcessRequest
	client := NewClient(WithProcessFactory(func(_ context.Context, request ProcessRequest) (Process, error) {
		requests = append(requests, request)
		return &fakeProcess{stdout: strings.NewReader(strings.Join([]string{
			`{"type":"system","subtype":"init","session_id":"` + requestSessionID(request) + `","model":"sonnet"}`,
			`{"type":"stream_event","session_id":"` + requestSessionID(request) + `","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}}`,
			`{"type":"stream_event","session_id":"` + requestSessionID(request) + `","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":" world"}}}`,
			`{"type":"result","subtype":"success","session_id":"` + requestSessionID(request) + `","result":"hello world"}`,
		}, "\n"))}, nil
	}))

	session, err := client.CreateSession(context.Background(), agent.CreateSessionRequest{Title: "demo", Directory: "/tmp/project"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	handle, err := client.Prompt(context.Background(), session.ID, "hello", agent.PromptWithDirectory("/tmp/project"), agent.PromptWithModel(agent.ModelRef{ProviderID: ClaudeProviderID, ModelID: "sonnet"}))
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	waitDone(t, handle)

	if got := len(requests); got != 1 {
		t.Fatalf("process request count = %d, want 1", got)
	}
	assertArgPair(t, requests[0].Args, "--session-id", session.ID)
	assertArgPair(t, requests[0].Args, "--model", "sonnet")
	if got, want := requests[0].Directory, "/tmp/project"; got != want {
		t.Fatalf("process directory = %q, want %q", got, want)
	}

	messages, err := client.PollMessagesAfter(context.Background(), session.ID, 0, agent.MessageOutputOptions{})
	if err != nil {
		t.Fatalf("PollMessagesAfter() error = %v", err)
	}
	if got, want := len(messages), 1; got != want {
		t.Fatalf("messages count = %d, want %d", got, want)
	}
	if got, want := messages[0].Content, "hello world"; got != want {
		t.Fatalf("message content = %q, want %q", got, want)
	}
	if got, want := messages[0].Model, (agent.ModelRef{ProviderID: ClaudeProviderID, ModelID: "sonnet"}); got != want {
		t.Fatalf("message model = %#v, want %#v", got, want)
	}

	latest, err := client.GetLatestAssistantMessage(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("GetLatestAssistantMessage() error = %v", err)
	}
	if latest == nil || latest.Content != "hello world" {
		t.Fatalf("latest message = %#v, want hello world", latest)
	}

	handle, err = client.Prompt(context.Background(), session.ID, "again")
	if err != nil {
		t.Fatalf("second Prompt() error = %v", err)
	}
	waitDone(t, handle)
	if got := len(requests); got != 2 {
		t.Fatalf("process request count = %d, want 2", got)
	}
	assertArgPair(t, requests[1].Args, "--resume", session.ID)
}

func TestClientListAndResolveModels(t *testing.T) {
	client := NewClient()

	models, err := client.ListModels(context.Background(), "")
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(models) == 0 {
		t.Fatalf("model count = 0, want defaults")
	}

	ref, err := client.ResolveModel(context.Background(), "claude/sonnet", "")
	if err != nil {
		t.Fatalf("ResolveModel() error = %v", err)
	}
	if got, want := ref, (agent.ModelRef{ProviderID: ClaudeProviderID, ModelID: "sonnet"}); got != want {
		t.Fatalf("ResolveModel() = %#v, want %#v", got, want)
	}

	ref, err = client.ResolveModel(context.Background(), "sonnet", "")
	if err != nil {
		t.Fatalf("ResolveModel() alias error = %v", err)
	}
	if got, want := ref, (agent.ModelRef{ProviderID: ClaudeProviderID, ModelID: "sonnet"}); got != want {
		t.Fatalf("ResolveModel() alias = %#v, want %#v", got, want)
	}

	ref, err = client.ResolveModel(context.Background(), "claude/claude-sonnet-4-6", "")
	if err != nil {
		t.Fatalf("ResolveModel() full id error = %v", err)
	}
	if got, want := ref, (agent.ModelRef{ProviderID: ClaudeProviderID, ModelID: "claude-sonnet-4-6"}); got != want {
		t.Fatalf("ResolveModel() full id = %#v, want %#v", got, want)
	}

	if _, err := client.ResolveModel(context.Background(), "missing", ""); err == nil {
		t.Fatalf("ResolveModel() missing error = nil, want error")
	}
}

func TestClientSessionListGetMessagesAndCursor(t *testing.T) {
	client := NewClient(WithProcessFactory(func(_ context.Context, request ProcessRequest) (Process, error) {
		return &fakeProcess{stdout: strings.NewReader(strings.Join([]string{
			`{"type":"stream_event","session_id":"` + requestSessionID(request) + `","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"answer"}}}`,
			`{"type":"result","subtype":"success","session_id":"` + requestSessionID(request) + `","result":"answer"}`,
		}, "\n"))}, nil
	}))

	session, err := client.CreateSession(context.Background(), agent.CreateSessionRequest{Title: "demo", Directory: "/tmp/project"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	sessions, err := client.ListSessions(context.Background(), "")
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if got, want := len(sessions), 1; got != want {
		t.Fatalf("session count = %d, want %d", got, want)
	}
	gotSession, err := client.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	if gotSession == nil || gotSession.Title != "demo" || gotSession.Directory != "/tmp/project" {
		t.Fatalf("GetSession() = %#v, want demo session", gotSession)
	}
	resumable, err := client.GetSession(context.Background(), "missing")
	if err != nil {
		t.Fatalf("GetSession() missing error = %v", err)
	}
	if resumable == nil || resumable.ID != "missing" {
		t.Fatalf("GetSession() missing = %#v, want resumable stub", resumable)
	}

	handle, err := client.Prompt(context.Background(), session.ID, "question")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	waitDone(t, handle)

	messages, err := client.GetMessages(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("GetMessages() error = %v", err)
	}
	if got, want := len(messages), 2; got != want {
		t.Fatalf("message count = %d, want %d", got, want)
	}
	if messages[0].Role != "user" || messages[1].Role != "assistant" {
		t.Fatalf("message roles = %s/%s, want user/assistant", messages[0].Role, messages[1].Role)
	}

	polled, err := client.PollMessagesAfter(context.Background(), session.ID, 0, agent.MessageOutputOptions{})
	if err != nil {
		t.Fatalf("PollMessagesAfter() error = %v", err)
	}
	if got, want := len(polled), 1; got != want {
		t.Fatalf("polled count = %d, want %d", got, want)
	}
	polled, err = client.PollMessagesAfter(context.Background(), session.ID, polled[0].CompletedAt, agent.MessageOutputOptions{})
	if err != nil {
		t.Fatalf("PollMessagesAfter() cursor error = %v", err)
	}
	if len(polled) != 0 {
		t.Fatalf("polled after cursor count = %d, want 0", len(polled))
	}
}

func TestClientPromptUnknownSessionResumes(t *testing.T) {
	var request ProcessRequest
	client := NewClient(WithProcessFactory(func(_ context.Context, current ProcessRequest) (Process, error) {
		request = current
		return &fakeProcess{stdout: strings.NewReader(`{"type":"result","subtype":"success","session_id":"existing-session","result":"done"}` + "\n")}, nil
	}))

	handle, err := client.Prompt(context.Background(), "existing-session", "continue")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	waitDone(t, handle)
	assertArgPair(t, request.Args, "--resume", "existing-session")
}

func TestClientPromptProcessError(t *testing.T) {
	client := NewClient(WithProcessFactory(func(context.Context, ProcessRequest) (Process, error) {
		return &fakeProcess{stdout: strings.NewReader(""), waitErr: io.ErrUnexpectedEOF}, nil
	}))
	session, err := client.CreateSession(context.Background(), agent.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	handle, err := client.Prompt(context.Background(), session.ID, "fail")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	select {
	case err := <-handle.Err():
		if err == nil {
			t.Fatalf("Prompt() err = nil, want error")
		}
	case <-handle.Done():
		t.Fatalf("Prompt() done, want error")
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for Prompt() error")
	}
}

func TestClientPromptStreamError(t *testing.T) {
	client := NewClient(WithProcessFactory(func(context.Context, ProcessRequest) (Process, error) {
		return &fakeProcess{stdout: strings.NewReader(`{"type":"error","session_id":"session-1","error":{"message":"auth failed"}}` + "\n")}, nil
	}))
	session, err := client.CreateSession(context.Background(), agent.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	handle, err := client.Prompt(context.Background(), session.ID, "fail")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	select {
	case err := <-handle.Err():
		if err == nil || !strings.Contains(err.Error(), "auth failed") {
			t.Fatalf("Prompt() err = %v, want auth failed", err)
		}
	case <-handle.Done():
		t.Fatalf("Prompt() done, want error")
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for Prompt() error")
	}
}

func TestClientListAgentsReturnsClaudeCode(t *testing.T) {
	client := NewClient()

	agents, err := client.ListAgents(context.Background(), "")
	if err != nil {
		t.Fatalf("ListAgents() error = %v", err)
	}
	if got, want := len(agents), 1; got != want {
		t.Fatalf("agent count = %d, want %d", got, want)
	}
	if got, want := agents[0].Name, "claude-code"; got != want {
		t.Fatalf("agent name = %q, want %q", got, want)
	}
}

func TestClientPendingInteractionsAreUnsupported(t *testing.T) {
	client := NewClient()

	permissions, err := client.ListPendingPermissions(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("ListPendingPermissions() error = %v", err)
	}
	if len(permissions) != 0 {
		t.Fatalf("permission count = %d, want 0", len(permissions))
	}
	questions, err := client.ListPendingQuestions(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("ListPendingQuestions() error = %v", err)
	}
	if len(questions) != 0 {
		t.Fatalf("question count = %d, want 0", len(questions))
	}
}

func TestClientConcurrentPromptReturnsBusy(t *testing.T) {
	reader, writer := io.Pipe()
	waitCh := make(chan error, 1)
	client := NewClient(WithProcessFactory(func(_ context.Context, request ProcessRequest) (Process, error) {
		return &fakeProcess{stdout: reader, waitCh: waitCh}, nil
	}))
	session, err := client.CreateSession(context.Background(), agent.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	handle, err := client.Prompt(context.Background(), session.ID, "first")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if _, err := client.Prompt(context.Background(), session.ID, "second"); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("concurrent Prompt() error = %v, want busy", err)
	}

	_, _ = writer.Write([]byte(`{"type":"result","subtype":"success","session_id":"` + session.ID + `","result":"done"}` + "\n"))
	_ = writer.Close()
	waitCh <- nil
	waitDone(t, handle)
}

type fakeProcess struct {
	stdout  io.Reader
	waitErr error
	waitCh  <-chan error
}

func (p *fakeProcess) Stdout() io.Reader { return p.stdout }

func (p *fakeProcess) Wait() error {
	if p.waitCh != nil {
		return <-p.waitCh
	}
	return p.waitErr
}

func (p *fakeProcess) Kill() error { return nil }

func waitDone(t *testing.T, handle *agent.PromptHandle) {
	t.Helper()
	select {
	case <-handle.Done():
	case err := <-handle.Err():
		t.Fatalf("Prompt() error = %v", err)
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for Prompt() done")
	}
}

func assertArgPair(t *testing.T, args []string, key string, want string) {
	t.Helper()
	for i := 0; i < len(args)-1; i++ {
		if args[i] == key && args[i+1] == want {
			return
		}
	}
	t.Fatalf("args %#v do not contain %s %s", args, key, want)
}

func requestSessionID(request ProcessRequest) string {
	for i := 0; i < len(request.Args)-1; i++ {
		if request.Args[i] == "--session-id" || request.Args[i] == "--resume" {
			return request.Args[i+1]
		}
	}
	return "session-1"
}
