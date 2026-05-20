package codex

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gitsang/agent-bridge/internal/agent"
)

func TestClientCreateSessionStartsThreadAndSetsTitle(t *testing.T) {
	transport := newFakeTransport(t)
	client := NewClient(WithTransportFactory(func(context.Context) (Transport, error) { return transport, nil }))

	transport.expectRequest("initialize", nil, map[string]any{
		"userAgent":      "codex-test",
		"codexHome":      "/tmp/codex",
		"platformFamily": "unix",
		"platformOs":     "linux",
	})
	transport.expectRequest("thread/start", func(params json.RawMessage) {
		var body struct {
			CWD       string `json:"cwd"`
			Ephemeral *bool  `json:"ephemeral"`
		}
		decodeJSON(t, params, &body)
		if got, want := body.CWD, "/tmp/project"; got != want {
			t.Fatalf("thread/start cwd = %q, want %q", got, want)
		}
		if body.Ephemeral == nil || *body.Ephemeral {
			t.Fatalf("thread/start ephemeral = %#v, want false", body.Ephemeral)
		}
	}, map[string]any{"thread": fakeThread("thread-1", "/tmp/project", "")})
	transport.expectRequest("thread/name/set", func(params json.RawMessage) {
		var body struct {
			ThreadID string `json:"threadId"`
			Name     string `json:"name"`
		}
		decodeJSON(t, params, &body)
		if got, want := body.ThreadID, "thread-1"; got != want {
			t.Fatalf("thread/name/set thread = %q, want %q", got, want)
		}
		if got, want := body.Name, "demo"; got != want {
			t.Fatalf("thread/name/set name = %q, want %q", got, want)
		}
	}, map[string]any{})

	session, err := client.CreateSession(context.Background(), agent.CreateSessionRequest{Title: "demo", Directory: "/tmp/project"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if got, want := session.ID, "thread-1"; got != want {
		t.Fatalf("session id = %q, want %q", got, want)
	}
	if got, want := session.Title, "demo"; got != want {
		t.Fatalf("session title = %q, want %q", got, want)
	}
	if got, want := session.Directory, "/tmp/project"; got != want {
		t.Fatalf("session directory = %q, want %q", got, want)
	}
	transport.assertDone()
}

func TestClientPromptAggregatesAssistantMessageDelta(t *testing.T) {
	transport := newFakeTransport(t)
	client := NewClient(WithTransportFactory(func(context.Context) (Transport, error) { return transport, nil }))

	transport.expectRequest("initialize", nil, map[string]any{})
	transport.expectRequest("thread/start", nil, map[string]any{"thread": fakeThread("thread-1", "/tmp/project", "")})
	_, err := client.CreateSession(context.Background(), agent.CreateSessionRequest{Directory: "/tmp/project"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	transport.expectRequest("turn/start", func(params json.RawMessage) {
		var body struct {
			ThreadID string `json:"threadId"`
			Input    []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"input"`
			CWD   string `json:"cwd"`
			Model string `json:"model"`
		}
		decodeJSON(t, params, &body)
		if got, want := body.ThreadID, "thread-1"; got != want {
			t.Fatalf("turn/start thread = %q, want %q", got, want)
		}
		if len(body.Input) != 1 || body.Input[0].Type != "text" || body.Input[0].Text != "hello" {
			t.Fatalf("turn/start input = %#v, want text hello", body.Input)
		}
		if got, want := body.CWD, "/tmp/project"; got != want {
			t.Fatalf("turn/start cwd = %q, want %q", got, want)
		}
		if got, want := body.Model, "gpt-5.5"; got != want {
			t.Fatalf("turn/start model = %q, want %q", got, want)
		}
	}, map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress", "items": []any{}}})
	handle, err := client.Prompt(context.Background(), "thread-1", "hello", agent.PromptWithDirectory("/tmp/project"), agent.PromptWithModel(agent.ModelRef{ProviderID: CodexProviderID, ModelID: "gpt-5.5"}))
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	transport.notify("item/agentMessage/delta", map[string]any{
		"threadId": "thread-1",
		"turnId":   "turn-1",
		"itemId":   "item-1",
		"delta":    "hello",
	})
	transport.notify("item/agentMessage/delta", map[string]any{
		"threadId": "thread-1",
		"turnId":   "turn-1",
		"itemId":   "item-1",
		"delta":    " world",
	})
	completedAt := float64(1710000000)
	transport.notify("turn/completed", map[string]any{
		"threadId": "thread-1",
		"turn": map[string]any{
			"id":          "turn-1",
			"status":      "completed",
			"items":       []any{},
			"completedAt": completedAt,
		},
	})

	waitDone(t, handle)

	messages, err := client.PollMessagesAfter(context.Background(), "thread-1", 0, agent.MessageOutputOptions{})
	if err != nil {
		t.Fatalf("PollMessagesAfter() error = %v", err)
	}
	if got, want := len(messages), 1; got != want {
		t.Fatalf("messages count = %d, want %d", got, want)
	}
	if got, want := messages[0].Content, "hello world"; got != want {
		t.Fatalf("message content = %q, want %q", got, want)
	}
	if got, want := messages[0].SessionID, "thread-1"; got != want {
		t.Fatalf("message session = %q, want %q", got, want)
	}
	if got, want := messages[0].Model.ModelID, "gpt-5.5"; got != want {
		t.Fatalf("message model = %q, want %q", got, want)
	}
	if got, want := messages[0].CompletedAt, completedAt; got != want {
		t.Fatalf("message completed = %v, want %v", got, want)
	}
	transport.assertDone()
}

func TestClientPermissionRequestAndReply(t *testing.T) {
	transport := newFakeTransport(t)
	client := NewClient(WithTransportFactory(func(context.Context) (Transport, error) { return transport, nil }))

	transport.expectRequest("initialize", nil, map[string]any{})
	transport.expectRequest("thread/start", nil, map[string]any{"thread": fakeThread("thread-1", "/tmp/project", "")})
	_, err := client.CreateSession(context.Background(), agent.CreateSessionRequest{Directory: "/tmp/project"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	transport.expectRequest("turn/start", nil, map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress", "items": []any{}}})
	handle, err := client.Prompt(context.Background(), "thread-1", "clean")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	transport.request("approval-1", "item/commandExecution/requestApproval", map[string]any{
		"threadId":    "thread-1",
		"turnId":      "turn-1",
		"itemId":      "cmd-1",
		"command":     "rm -rf build",
		"cwd":         "/tmp/project",
		"reason":      "cleanup",
		"startedAtMs": 1710000000000,
	})
	transport.notify("turn/completed", map[string]any{
		"threadId": "thread-1",
		"turn":     map[string]any{"id": "turn-1", "status": "completed", "items": []any{}, "completedAt": float64(1710000001)},
	})

	requests, err := waitPermissions(t, client, "thread-1")
	if err != nil {
		t.Fatalf("ListPendingPermissions() error = %v", err)
	}
	if got, want := len(requests), 1; got != want {
		t.Fatalf("permission count = %d, want %d", got, want)
	}
	if got, want := requests[0].ID, "approval-1"; got != want {
		t.Fatalf("permission id = %q, want %q", got, want)
	}
	if !strings.Contains(requests[0].Permission, "rm -rf build") || !strings.Contains(requests[0].Permission, "cleanup") {
		t.Fatalf("permission label = %q, want command and reason", requests[0].Permission)
	}
	if len(requests[0].Patterns) != 1 || requests[0].Patterns[0] != "/tmp/project" {
		t.Fatalf("permission patterns = %#v, want cwd", requests[0].Patterns)
	}

	if err := client.ReplyPermission(context.Background(), "thread-1", "approval-1", agent.PermissionReplyAlways); err != nil {
		t.Fatalf("ReplyPermission() error = %v", err)
	}
	response := transport.nextClientResponse()
	if got, want := response.ID, json.RawMessage(`"approval-1"`); string(got) != string(want) {
		t.Fatalf("response id = %s, want %s", got, want)
	}
	var body struct {
		Decision string `json:"decision"`
	}
	decodeJSON(t, response.Result, &body)
	if got, want := body.Decision, "acceptForSession"; got != want {
		t.Fatalf("approval decision = %q, want %q", got, want)
	}

	waitDone(t, handle)
	transport.assertDone()
}

func TestClientQuestionRequestAndReply(t *testing.T) {
	transport := newFakeTransport(t)
	client := NewClient(WithTransportFactory(func(context.Context) (Transport, error) { return transport, nil }))

	transport.expectRequest("initialize", nil, map[string]any{})
	transport.expectRequest("thread/start", nil, map[string]any{"thread": fakeThread("thread-1", "/tmp/project", "")})
	_, err := client.CreateSession(context.Background(), agent.CreateSessionRequest{Directory: "/tmp/project"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	transport.expectRequest("turn/start", nil, map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress", "items": []any{}}})
	handle, err := client.Prompt(context.Background(), "thread-1", "deploy")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	transport.request("question-1", "item/tool/requestUserInput", map[string]any{
		"threadId": "thread-1",
		"turnId":   "turn-1",
		"itemId":   "ask-1",
		"questions": []any{
			map[string]any{
				"id":       "env",
				"header":   "Deploy",
				"question": "Environment?",
				"options": []any{
					map[string]any{"label": "staging", "description": "Staging"},
					map[string]any{"label": "production", "description": "Production"},
				},
			},
		},
	})
	transport.notify("turn/completed", map[string]any{
		"threadId": "thread-1",
		"turn":     map[string]any{"id": "turn-1", "status": "completed", "items": []any{}, "completedAt": float64(1710000001)},
	})

	requests, err := waitQuestions(t, client, "thread-1")
	if err != nil {
		t.Fatalf("ListPendingQuestions() error = %v", err)
	}
	if got, want := len(requests), 1; got != want {
		t.Fatalf("question count = %d, want %d", got, want)
	}
	question := requests[0].Questions[0]
	if got, want := question.Text, "Environment?"; got != want {
		t.Fatalf("question text = %q, want %q", got, want)
	}
	if len(question.Options) != 2 || question.Options[1] != "production" {
		t.Fatalf("question options = %#v, want staging/production", question.Options)
	}

	if err := client.ReplyQuestion(context.Background(), "thread-1", "question-1", [][]string{{"production"}}); err != nil {
		t.Fatalf("ReplyQuestion() error = %v", err)
	}
	response := transport.nextClientResponse()
	var body struct {
		Answers map[string]struct {
			Answers []string `json:"answers"`
		} `json:"answers"`
	}
	decodeJSON(t, response.Result, &body)
	if got := body.Answers["env"].Answers; len(got) != 1 || got[0] != "production" {
		t.Fatalf("answers = %#v, want production", got)
	}

	waitDone(t, handle)
	transport.assertDone()
}

func TestClientContextCancelInterruptsTurn(t *testing.T) {
	transport := newFakeTransport(t)
	client := NewClient(WithTransportFactory(func(context.Context) (Transport, error) { return transport, nil }))

	transport.expectRequest("initialize", nil, map[string]any{})
	transport.expectRequest("thread/start", nil, map[string]any{"thread": fakeThread("thread-1", "/tmp/project", "")})
	_, err := client.CreateSession(context.Background(), agent.CreateSessionRequest{Directory: "/tmp/project"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	transport.expectRequest("turn/start", nil, map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress", "items": []any{}}})
	promptCtx, cancel := context.WithCancel(context.Background())
	handle, err := client.Prompt(promptCtx, "thread-1", "wait")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	cancel()
	transport.expectRequest("turn/interrupt", func(params json.RawMessage) {
		var body struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
		}
		decodeJSON(t, params, &body)
		if got, want := body.ThreadID, "thread-1"; got != want {
			t.Fatalf("turn/interrupt thread = %q, want %q", got, want)
		}
		if got, want := body.TurnID, "turn-1"; got != want {
			t.Fatalf("turn/interrupt turn = %q, want %q", got, want)
		}
	}, map[string]any{})

	select {
	case err := <-handle.Err():
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("Prompt() err = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for prompt cancel error")
	}
	transport.assertDone()
}

func TestClientListAndResolveModels(t *testing.T) {
	transport := newFakeTransport(t)
	client := NewClient(WithTransportFactory(func(context.Context) (Transport, error) { return transport, nil }))

	transport.expectRequest("initialize", nil, map[string]any{})
	transport.expectRequest("model/list", nil, map[string]any{
		"data": []any{
			map[string]any{"id": "gpt-5.5", "model": "gpt-5.5", "displayName": "GPT 5.5"},
			map[string]any{"id": "gpt-5.4", "model": "gpt-5.4", "displayName": "GPT 5.4"},
		},
	})

	models, err := client.ListModels(context.Background(), "")
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if got, want := len(models), 2; got != want {
		t.Fatalf("model count = %d, want %d", got, want)
	}
	if got, want := models[0].ProviderID, CodexProviderID; got != want {
		t.Fatalf("provider = %q, want %q", got, want)
	}
	if got, want := models[0].ModelID, "gpt-5.4"; got != want {
		t.Fatalf("model = %q, want %q", got, want)
	}

	ref, err := client.ResolveModel(context.Background(), "codex/gpt-5.4", "")
	if err != nil {
		t.Fatalf("ResolveModel() error = %v", err)
	}
	if got, want := ref, (agent.ModelRef{ProviderID: CodexProviderID, ModelID: "gpt-5.4"}); got != want {
		t.Fatalf("ResolveModel() = %#v, want %#v", got, want)
	}
	transport.assertDone()
}

func TestClientMessageOutputOptionsFilterKinds(t *testing.T) {
	transport := newFakeTransport(t)
	client := NewClient(WithTransportFactory(func(context.Context) (Transport, error) { return transport, nil }))

	transport.expectRequest("initialize", nil, map[string]any{})
	transport.expectRequest("thread/start", nil, map[string]any{"thread": fakeThread("thread-1", "/tmp/project", "")})
	_, err := client.CreateSession(context.Background(), agent.CreateSessionRequest{Directory: "/tmp/project"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	transport.expectRequest("turn/start", nil, map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress", "items": []any{}}})
	handle, err := client.Prompt(context.Background(), "thread-1", "hello")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	transport.notify("item/reasoning/textDelta", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "itemId": "reason-1", "delta": "hidden"})
	transport.notify("item/commandExecution/outputDelta", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "itemId": "cmd-1", "delta": "go test ./..."})
	transport.notify("turn/diff/updated", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "diff": "diff --git a/a b/a"})
	transport.notify("item/agentMessage/delta", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "itemId": "msg-1", "delta": "answer"})
	transport.notify("turn/completed", map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": "turn-1", "status": "completed", "items": []any{}, "completedAt": float64(1710000001)}})

	waitDone(t, handle)

	messages, err := client.PollMessagesAfter(context.Background(), "thread-1", 0, agent.MessageOutputOptions{Include: []agent.MessageContentKind{agent.MessageContentAnswer}})
	if err != nil {
		t.Fatalf("PollMessagesAfter() error = %v", err)
	}
	if got, want := messages[0].Content, "answer"; got != want {
		t.Fatalf("filtered content = %q, want %q", got, want)
	}
	transport.assertDone()
}

type fakeTransport struct {
	t            *testing.T
	incoming     chan json.RawMessage
	outgoing     chan jsonRPCMessage
	expectations chan expectedRequest
	closed       chan struct{}
	wg           sync.WaitGroup
}

type expectedRequest struct {
	method string
	check  func(json.RawMessage)
	result any
}

func newFakeTransport(t *testing.T) *fakeTransport {
	return &fakeTransport{
		t:            t,
		incoming:     make(chan json.RawMessage, 64),
		outgoing:     make(chan jsonRPCMessage, 64),
		expectations: make(chan expectedRequest, 64),
		closed:       make(chan struct{}),
	}
}

func (t *fakeTransport) ReadMessage(context.Context) (json.RawMessage, error) {
	select {
	case msg := <-t.incoming:
		return msg, nil
	case <-t.closed:
		return nil, io.EOF
	case <-time.After(time.Second):
		return nil, io.EOF
	}
}

func (t *fakeTransport) WriteMessage(_ context.Context, msg json.RawMessage) error {
	var parsed jsonRPCMessage
	decodeJSON(t.t, msg, &parsed)
	if parsed.Method != "" && parsed.ID != nil {
		select {
		case expected := <-t.expectations:
			if got := strings.TrimSpace(parsed.Method); got != expected.method {
				t.t.Fatalf("client method = %q, want %q", got, expected.method)
			}
			if expected.check != nil {
				expected.check(parsed.Params)
			}
			t.response(parsed.ID, expected.result)
			t.wg.Done()
		case <-time.After(time.Second):
			t.t.Fatalf("unexpected client request: %#v", parsed)
		}
		return nil
	}
	t.outgoing <- parsed
	return nil
}

func (t *fakeTransport) Close() error {
	select {
	case <-t.closed:
	default:
		close(t.closed)
	}
	return nil
}

func (t *fakeTransport) expectRequest(method string, check func(json.RawMessage), result any) {
	t.wg.Add(1)
	t.expectations <- expectedRequest{method: method, check: check, result: result}
}

func (t *fakeTransport) nextClientMessage() jsonRPCMessage {
	t.t.Helper()
	select {
	case msg := <-t.outgoing:
		return msg
	case <-time.After(time.Second):
		t.t.Fatalf("timed out waiting for client JSON-RPC message")
		return jsonRPCMessage{}
	}
}

func (t *fakeTransport) nextClientResponse() jsonRPCMessage {
	t.t.Helper()
	msg := t.nextClientMessage()
	if msg.ID == nil || msg.Result == nil {
		t.t.Fatalf("client message = %#v, want JSON-RPC response", msg)
	}
	return msg
}

func (t *fakeTransport) response(id json.RawMessage, result any) {
	payload, err := json.Marshal(jsonRPCMessage{ID: id, Result: mustRaw(t.t, result)})
	if err != nil {
		t.t.Fatalf("marshal response: %v", err)
	}
	t.incoming <- payload
}

func (t *fakeTransport) notify(method string, params any) {
	payload, err := json.Marshal(jsonRPCMessage{Method: method, Params: mustRaw(t.t, params)})
	if err != nil {
		t.t.Fatalf("marshal notification: %v", err)
	}
	t.incoming <- payload
}

func (t *fakeTransport) request(id string, method string, params any) {
	payload, err := json.Marshal(jsonRPCMessage{ID: mustRaw(t.t, id), Method: method, Params: mustRaw(t.t, params)})
	if err != nil {
		t.t.Fatalf("marshal request: %v", err)
	}
	t.incoming <- payload
}

func (t *fakeTransport) assertDone() {
	t.t.Helper()
	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.t.Fatalf("timed out waiting for expected requests")
	}
	select {
	case msg := <-t.outgoing:
		t.t.Fatalf("unexpected client message: %#v", msg)
	default:
	}
	select {
	case expected := <-t.expectations:
		t.t.Fatalf("unconsumed expected request: %s", expected.method)
	default:
	}
}

func fakeThread(id, cwd, title string) map[string]any {
	return map[string]any{
		"id":            id,
		"sessionId":     id,
		"forkedFromId":  nil,
		"preview":       title,
		"ephemeral":     false,
		"modelProvider": CodexProviderID,
		"createdAt":     float64(1710000000),
		"updatedAt":     float64(1710000000),
		"status":        "idle",
		"path":          nil,
		"cwd":           cwd,
		"cliVersion":    "test",
		"source":        "unknown",
		"threadSource":  nil,
		"agentNickname": nil,
		"agentRole":     nil,
		"gitInfo":       nil,
		"name":          title,
		"turns":         []any{},
	}
}

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

func waitPermissions(t *testing.T, client *Client, sessionID string) ([]agent.PermissionRequest, error) {
	t.Helper()
	for i := 0; i < 10; i++ {
		requests, err := client.ListPendingPermissions(context.Background(), sessionID)
		if err != nil || len(requests) > 0 {
			return requests, err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return client.ListPendingPermissions(context.Background(), sessionID)
}

func waitQuestions(t *testing.T, client *Client, sessionID string) ([]agent.QuestionRequest, error) {
	t.Helper()
	for i := 0; i < 10; i++ {
		requests, err := client.ListPendingQuestions(context.Background(), sessionID)
		if err != nil || len(requests) > 0 {
			return requests, err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return client.ListPendingQuestions(context.Background(), sessionID)
}

func mustRaw(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal raw: %v", err)
	}
	return data
}

func decodeJSON(t *testing.T, data json.RawMessage, target any) {
	t.Helper()
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("decode JSON %s: %v", data, err)
	}
}
