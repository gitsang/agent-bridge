package acp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gitsang/agent-bridge/internal/types"
)

// fakeTransport drives the driver from a scripted ACP agent without spawning a
// process. The driver reads agent records from toDriver; records the driver
// writes accumulate in written and are consumed by predicate, so the scripted
// agent and the assertions never steal each other's records.
type fakeTransport struct {
	mu       sync.Mutex
	toDriver chan json.RawMessage
	written  []rpcMessage
	closed   chan struct{}
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{
		toDriver: make(chan json.RawMessage, 64),
		closed:   make(chan struct{}),
	}
}

func (f *fakeTransport) ReadMessage(ctx context.Context) (json.RawMessage, error) {
	select {
	case raw := <-f.toDriver:
		return raw, nil
	case <-f.closed:
		return nil, context.Canceled
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f *fakeTransport) WriteMessage(_ context.Context, msg json.RawMessage) error {
	var record rpcMessage
	if err := json.Unmarshal(msg, &record); err != nil {
		return err
	}
	f.mu.Lock()
	f.written = append(f.written, record)
	f.mu.Unlock()
	return nil
}

func (f *fakeTransport) Close() error {
	select {
	case <-f.closed:
	default:
		close(f.closed)
	}
	return nil
}

// take removes and returns the first written record matching predicate.
func (f *fakeTransport) take(match func(rpcMessage) bool) (rpcMessage, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for index, record := range f.written {
		if !match(record) {
			continue
		}
		f.written = append(f.written[:index], f.written[index+1:]...)
		return record, true
	}
	return rpcMessage{}, false
}

func (f *fakeTransport) send(t *testing.T, record any) {
	t.Helper()
	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	select {
	case f.toDriver <- payload:
	case <-time.After(time.Second):
		t.Fatalf("fake transport blocked sending %s", payload)
	}
}

func (f *fakeTransport) response(id any, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

// waitFor blocks until the driver wrote a matching record.
func (f *fakeTransport) waitFor(t *testing.T, match func(rpcMessage) bool) rpcMessage {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if record, ok := f.take(match); ok {
			return record
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no matching record was written")
	return rpcMessage{}
}

// serveFakeAgent answers driver requests using handler, which returns the result
// payload for a method, or nil to fall back to the default fake behaviour.
func serveFakeAgent(t *testing.T, transport *fakeTransport, handler func(method string, params json.RawMessage, id json.RawMessage) (any, bool)) {
	t.Helper()
	go func() {
		for {
			select {
			case <-transport.closed:
				return
			default:
			}
			request, ok := transport.take(func(record rpcMessage) bool {
				return record.Method != "" && record.ID != nil
			})
			if !ok {
				time.Sleep(2 * time.Millisecond)
				continue
			}
			if result, handled := handler(request.Method, request.Params, request.ID); handled && result != nil {
				transport.send(t, transport.response(request.ID, result))
			}
		}
	}()
}

func defaultFakeAgent(t *testing.T, transport *fakeTransport, models []any) {
	t.Helper()
	serveFakeAgent(t, transport, func(method string, params json.RawMessage, id json.RawMessage) (any, bool) {
		switch method {
		case "initialize":
			return map[string]any{
				"protocolVersion": 1,
				"agentCapabilities": map[string]any{
					"sessionCapabilities": map[string]any{"list": map[string]any{}, "close": map[string]any{}},
				},
			}, true
		case "session/new":
			return map[string]any{
				"sessionId": "fake-session-1",
				"configOptions": []any{map[string]any{
					"id":           "model",
					"category":     "model",
					"type":         "select",
					"currentValue": "provider/model-a",
					"options":      models,
				}},
			}, true
		case "session/set_config_option":
			return map[string]any{"configOptions": []any{map[string]any{
				"id":           "model",
				"category":     "model",
				"type":         "select",
				"currentValue": "provider/model-a",
				"options":      models,
			}}}, true
		case "session/close":
			return map[string]any{}, true
		case "session/list":
			return map[string]any{"sessions": []any{map[string]any{
				"sessionId": "fake-session-1",
				"cwd":       "/tmp/project",
				"title":     "Fake session",
				"updatedAt": "2026-01-02T03:04:05Z",
			}}}, true
		case "session/prompt":
			transport.send(t, map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": "fake-session-1",
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": "hello"},
					},
				},
			})
			transport.send(t, map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": "fake-session-1",
					"update": map[string]any{
						"sessionUpdate": "agent_thought_chunk",
						"content":       map[string]any{"type": "text", "text": "thinking"},
					},
				},
			})
			return map[string]any{"stopReason": "end_turn"}, true
		}
		return nil, false
	})
}

func newTestAgent(transport *fakeTransport) *Agent {
	client := NewClient(
		WithTransportFactory(func(context.Context, string) (Transport, error) { return transport, nil }),
		WithTimeout(10*time.Second),
	)
	client.directory = "/tmp/project"
	return client
}

func TestListModelsReadsSessionConfigOptions(t *testing.T) {
	transport := newFakeTransport()
	defaultFakeAgent(t, transport, []any{
		map[string]any{"value": "provider/model-a", "name": "Model A"},
		map[string]any{"value": "provider/model-b", "name": "Model B"},
	})
	client := newTestAgent(transport)

	models, err := client.ListModels(context.Background(), "/tmp/project")
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("ListModels() returned %d models, want 2", len(models))
	}
	if models[0].String() != "provider/model-a" {
		t.Fatalf("models[0] = %s, want provider/model-a", models[0])
	}
	if models[0].ModelName != "Model A" {
		t.Fatalf("models[0].ModelName = %q, want Model A", models[0].ModelName)
	}
}

func TestPromptAggregatesUpdates(t *testing.T) {
	transport := newFakeTransport()
	defaultFakeAgent(t, transport, nil)
	client := newTestAgent(transport)

	session, err := client.CreateSession(context.Background(), types.CreateSessionRequest{Directory: "/tmp/project"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	handle, err := client.Prompt(context.Background(), session.ID, "hi")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	select {
	case <-handle.Done():
	case err := <-handle.Err():
		t.Fatalf("Prompt() failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt() did not finish")
	}

	messages, err := client.PollMessagesAfter(context.Background(), session.ID, 0, types.MessageOutputOptions{})
	if err != nil {
		t.Fatalf("PollMessagesAfter() error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("PollMessagesAfter() returned %d messages, want 1", len(messages))
	}
	if messages[0].Content != "hello" {
		t.Fatalf("Content = %q, want hello", messages[0].Content)
	}
	if messages[0].Reasoning != "thinking" {
		t.Fatalf("Reasoning = %q, want thinking", messages[0].Reasoning)
	}
}

func TestPromptHonoursOutputFilter(t *testing.T) {
	transport := newFakeTransport()
	defaultFakeAgent(t, transport, nil)
	client := newTestAgent(transport)

	session, err := client.CreateSession(context.Background(), types.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	handle, err := client.Prompt(context.Background(), session.ID, "hi")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	select {
	case <-handle.Done():
	case err := <-handle.Err():
		t.Fatalf("Prompt() failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt() did not finish")
	}

	output := types.MessageOutputOptions{Include: []types.MessageContentKind{types.MessageContentAnswer}}
	messages, err := client.PollMessagesAfter(context.Background(), session.ID, 0, output)
	if err != nil {
		t.Fatalf("PollMessagesAfter() error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("PollMessagesAfter() returned %d messages, want 1", len(messages))
	}
	if messages[0].Reasoning != "" {
		t.Fatalf("Reasoning = %q, want filtered out", messages[0].Reasoning)
	}
}

func TestListSessionsUsesAgentCapability(t *testing.T) {
	transport := newFakeTransport()
	defaultFakeAgent(t, transport, nil)
	client := newTestAgent(transport)

	sessions, err := client.ListSessions(context.Background(), "/tmp/project")
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("ListSessions() returned %d sessions, want 1", len(sessions))
	}
	if sessions[0].ID != "fake-session-1" || sessions[0].Title != "Fake session" {
		t.Fatalf("unexpected session: %+v", sessions[0])
	}
	if sessions[0].UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt was not parsed")
	}
}

func TestReplyPermissionSelectsMatchingOption(t *testing.T) {
	transport := newFakeTransport()
	serveFakeAgent(t, transport, func(method string, params json.RawMessage, id json.RawMessage) (any, bool) {
		switch method {
		case "initialize":
			return map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}}, true
		case "session/new":
			return map[string]any{"sessionId": "fake-session-1"}, true
		case "session/prompt":
			transport.send(t, map[string]any{
				"jsonrpc": "2.0",
				"id":      99,
				"method":  "session/request_permission",
				"params": map[string]any{
					"sessionId": "fake-session-1",
					"toolCall":  map[string]any{"toolCallId": "call-1", "title": "Run tests"},
					"options": []any{
						map[string]any{"optionId": "allow-once", "name": "Allow once", "kind": "allow_once"},
						map[string]any{"optionId": "allow-always", "name": "Always allow", "kind": "allow_always"},
						map[string]any{"optionId": "reject-once", "name": "Reject", "kind": "reject_once"},
					},
				},
			})
			return map[string]any{"stopReason": "end_turn"}, true
		}
		return nil, false
	})
	client := newTestAgent(transport)

	session, err := client.CreateSession(context.Background(), types.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	handle, err := client.Prompt(context.Background(), session.ID, "run tests")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	var requests []types.PermissionRequest
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		requests, err = client.ListPendingPermissions(context.Background(), session.ID)
		if err == nil && len(requests) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(requests) != 1 {
		t.Fatalf("ListPendingPermissions() returned %d requests, want 1", len(requests))
	}
	if requests[0].Permission != "Run tests" {
		t.Fatalf("Permission = %q, want Run tests", requests[0].Permission)
	}

	if err := client.ReplyPermission(context.Background(), session.ID, requests[0].ID, types.PermissionReplyAlways); err != nil {
		t.Fatalf("ReplyPermission() error = %v", err)
	}
	response := transport.waitFor(t, func(record rpcMessage) bool {
		return string(record.ID) == "99" && record.Method == ""
	})
	if !strings.Contains(string(response.Result), `"allow-always"`) {
		t.Fatalf("permission reply = %s, want allow-always option", response.Result)
	}
	if err := client.ReplyPermission(context.Background(), session.ID, requests[0].ID, types.PermissionReplyOnce); err == nil {
		t.Fatal("expected ErrInteractionNoLongerPending for a settled request")
	}

	select {
	case <-handle.Done():
	case err := <-handle.Err():
		t.Fatalf("Prompt() failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt() did not finish")
	}
}

func TestPromptCancellationNotifiesAgent(t *testing.T) {
	transport := newFakeTransport()
	serveFakeAgent(t, transport, func(method string, params json.RawMessage, id json.RawMessage) (any, bool) {
		switch method {
		case "initialize":
			return map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}}, true
		case "session/new":
			return map[string]any{"sessionId": "fake-session-1"}, true
		case "session/prompt":
			// Never answer: the driver must time out and cancel.
			return nil, false
		}
		return nil, false
	})
	client := NewClient(
		WithTransportFactory(func(context.Context, string) (Transport, error) { return transport, nil }),
		WithTimeout(50*time.Millisecond),
	)

	session, err := client.CreateSession(context.Background(), types.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	handle, err := client.Prompt(context.Background(), session.ID, "hang")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	select {
	case err := <-handle.Err():
		if err == nil {
			t.Fatal("expected an error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Prompt() did not time out")
	}

	cancel := transport.waitFor(t, func(record rpcMessage) bool {
		return record.Method == "session/cancel"
	})
	if !strings.Contains(string(cancel.Params), "fake-session-1") {
		t.Fatalf("cancel params = %s, want session id", cancel.Params)
	}
}

func TestParseModelValue(t *testing.T) {
	cases := []struct {
		raw  string
		want types.ModelRef
	}{
		{"provider/model", types.ModelRef{ProviderID: "provider", ModelID: "model"}},
		{"provider/model:variant", types.ModelRef{ProviderID: "provider", ModelID: "model:variant"}},
		{"model", types.ModelRef{ProviderID: ProviderID, ModelID: "model"}},
		{`["provider","model"]`, types.ModelRef{ProviderID: "provider", ModelID: "model"}},
		{"", types.ModelRef{}},
	}
	for _, tc := range cases {
		if got := parseModelValue(tc.raw); got != tc.want {
			t.Fatalf("parseModelValue(%q) = %+v, want %+v", tc.raw, got, tc.want)
		}
	}
}

func TestPickPermissionOption(t *testing.T) {
	options := map[string]string{
		"a": "allow_once",
		"b": "allow_always",
		"c": "reject_once",
	}
	if got := pickPermissionOption(options, types.PermissionReplyOnce); got != "a" {
		t.Fatalf("once = %q, want a", got)
	}
	if got := pickPermissionOption(options, types.PermissionReplyAlways); got != "b" {
		t.Fatalf("always = %q, want b", got)
	}
	if got := pickPermissionOption(options, types.PermissionReplyReject); got != "c" {
		t.Fatalf("reject = %q, want c", got)
	}
	if got := pickPermissionOption(map[string]string{"a": "reject_once"}, types.PermissionReplyOnce); got != "" {
		t.Fatalf("missing option = %q, want empty", got)
	}
}
