package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gitsang/agent-bridge/internal/types"
)

// fakePi plays the scripted side of `pi --mode rpc` over in-memory pipes.
type fakePi struct {
	mu     sync.Mutex
	writes []map[string]any

	toDriver   *io.PipeWriter
	driverRead *bufio.Reader

	// handler answers a command; a nil return means "respond with success and
	// no data".
	handler func(pi *fakePi, command string, fields map[string]any)
}

// newFakePi wires a fake agent into a transport without spawning a process. The
// returned agent's fake is exposed through fakeOf for assertions.
func newFakePi(t *testing.T, handler func(pi *fakePi, command string, fields map[string]any)) *Agent {
	t.Helper()

	driverToPiReader, driverToPiWriter := io.Pipe()
	piToDriverReader, piToDriverWriter := io.Pipe()

	fake := &fakePi{
		toDriver:   piToDriverWriter,
		driverRead: bufio.NewReader(driverToPiReader),
		handler:    handler,
	}
	go fake.serve()

	agent := NewClient(
		WithTransportFactory(func(context.Context, string) (*processTransport, error) {
			return &processTransport{
				stdin:  driverToPiWriter,
				stdout: bufio.NewReader(piToDriverReader),
			}, nil
		}),
		WithTimeout(10*time.Second),
	)
	t.Cleanup(func() {
		_ = driverToPiWriter.Close()
		_ = piToDriverWriter.Close()
	})
	fakes.Store(agent, fake)
	return agent
}

// fakes lets assertions reach the scripted agent behind an Agent value.
var fakes sync.Map

func fakeOf(agent *Agent) *fakePi {
	if value, ok := fakes.Load(agent); ok {
		return value.(*fakePi)
	}
	return nil
}

func (f *fakePi) serve() {
	for {
		line, err := f.driverRead.ReadString('\n')
		if err != nil {
			return
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal([]byte(trimmed), &fields); err != nil {
			return
		}
		f.mu.Lock()
		f.writes = append(f.writes, fields)
		f.mu.Unlock()

		command, _ := fields["type"].(string)
		id, _ := fields["id"].(string)
		if command == "extension_ui_response" {
			continue
		}
		// A handler answers commands it cares about; anything else succeeds
		// with no data, matching pi's behaviour for state-changing commands.
		if f.handler != nil {
			f.handler(f, command, fields)
			continue
		}
		if id != "" {
			f.respond(id, command, map[string]any{})
		}
	}
}

// send writes a record to the driver. Scripted goroutines keep running after a
// test finishes, so a write to a closed pipe is not a failure.
func (f *fakePi) send(record map[string]any) {
	payload, err := json.Marshal(record)
	if err != nil {
		return
	}
	_, _ = f.toDriver.Write(append(payload, '\n'))
}

func (f *fakePi) respond(id, command string, data map[string]any) {
	record := map[string]any{
		"id":      id,
		"type":    "response",
		"command": command,
		"success": true,
	}
	if data != nil {
		record["data"] = data
	}
	payload, _ := json.Marshal(record)
	_, _ = f.toDriver.Write(append(payload, '\n'))
}

func (f *fakePi) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	commands := make([]string, 0, len(f.writes))
	for _, write := range f.writes {
		if command, ok := write["type"].(string); ok {
			commands = append(commands, command)
		}
	}
	return commands
}

func (f *fakePi) lastWrite() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) == 0 {
		return nil
	}
	return f.writes[len(f.writes)-1]
}

func waitForCommand(t *testing.T, fake *fakePi, command string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, seen := range fake.commands() {
			if seen == command {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("command %q was never written", command)
}

func TestListModelsUsesProviderAndModelIDs(t *testing.T) {
	agent := newFakePi(t, func(f *fakePi, command string, fields map[string]any) {
		if command != "get_available_models" {
			return
		}
		f.respond(fields["id"].(string), command, map[string]any{
			"models": []any{
				map[string]any{"id": "m2", "name": "Model Two", "provider": "prov"},
				map[string]any{"id": "m1", "name": "Model One", "provider": "prov"},
			},
		})
	})

	models, err := agent.ListModels(context.Background(), "")
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("ListModels() returned %d models, want 2", len(models))
	}
	if models[0].String() != "prov/m1" {
		t.Fatalf("models[0] = %s, want prov/m1", models[0])
	}
	if models[0].ModelName != "Model One" {
		t.Fatalf("ModelName = %q, want Model One", models[0].ModelName)
	}
}

func TestResolveModelAcceptsThinkingSuffixAndIndex(t *testing.T) {
	agent := newFakePi(t, func(f *fakePi, command string, fields map[string]any) {
		if command != "get_available_models" {
			return
		}
		f.respond(fields["id"].(string), command, map[string]any{
			"models": []any{map[string]any{"id": "sonnet", "provider": "anthropic"}},
		})
	})

	ref, err := agent.ResolveModel(context.Background(), "sonnet:high", "")
	if err != nil {
		t.Fatalf("ResolveModel() error = %v", err)
	}
	if ref.String() != "anthropic/sonnet" {
		t.Fatalf("ResolveModel() = %s, want anthropic/sonnet", ref)
	}

	ref, err = agent.ResolveModel(context.Background(), "anthropic/opus", "")
	if err != nil {
		t.Fatalf("ResolveModel() error = %v", err)
	}
	if ref.String() != "anthropic/opus" {
		t.Fatalf("ResolveModel() = %s, want anthropic/opus", ref)
	}
}

func TestCreateSessionNamesTheSession(t *testing.T) {
	agent := newFakePi(t, func(f *fakePi, command string, fields map[string]any) {
		switch command {
		case "get_state":
			f.respond(fields["id"].(string), command, map[string]any{
				"sessionId":   "ses_1",
				"sessionFile": "/tmp/ses_1.jsonl",
			})
		case "set_session_name":
			f.respond(fields["id"].(string), command, nil)
		}
	})

	session, err := agent.CreateSession(context.Background(), types.CreateSessionRequest{
		Directory: "/tmp/project",
		Title:     "my work",
	})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if session.ID != "ses_1" || session.Title != "my work" || session.Directory != "/tmp/project" {
		t.Fatalf("unexpected session: %+v", session)
	}
	fake := fakeOf(agent)
	if fake == nil {
		t.Fatal("fake pi was not wired")
	}
	waitForCommand(t, fake, "set_session_name")
	last := fake.lastWrite()
	if last["name"] != "my work" {
		t.Fatalf("set_session_name name = %v, want my work", last["name"])
	}
}

func TestPromptAggregatesMessagesAndTools(t *testing.T) {
	agent := newFakePi(t, func(f *fakePi, command string, fields map[string]any) {
		switch command {
		case "get_state":
			f.respond(fields["id"].(string), command, map[string]any{"sessionId": "ses_1"})
		case "prompt":
			f.respond(fields["id"].(string), command, map[string]any{"disposition": "started"})
			go func() {
				f.send(map[string]any{"type": "agent_start"})
				f.send(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant"}})
				f.send(map[string]any{
					"type": "message_update",
					"assistantMessageEvent": map[string]any{
						"type":  "text_delta",
						"delta": "po",
					},
				})
				f.send(map[string]any{
					"type": "message_end",
					"message": map[string]any{
						"role":       "assistant",
						"provider":   "prov",
						"model":      "m1",
						"stopReason": "stop",
						"content": []any{
							map[string]any{"type": "text", "text": "pong"},
							map[string]any{"type": "thinking", "thinking": "thought"},
							map[string]any{"type": "toolCall", "id": "call_1", "name": "bash",
								"arguments": map[string]any{"command": "ls"}},
						},
					},
				})
				f.send(map[string]any{
					"type":       "tool_execution_end",
					"toolCallId": "call_1",
					"toolName":   "bash",
					"result": map[string]any{
						"content": []any{map[string]any{"type": "text", "text": "file.txt"}},
					},
				})
				f.send(map[string]any{"type": "turn_end"})
				f.send(map[string]any{"type": "agent_end"})
				f.send(map[string]any{"type": "agent_settled", "aborted": false})
			}()
		}
	})

	session, err := agent.CreateSession(context.Background(), types.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	handle, err := agent.Prompt(context.Background(), session.ID, "hi")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	select {
	case <-handle.Done():
	case err := <-handle.Err():
		t.Fatalf("Prompt() failed: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("Prompt() did not settle")
	}

	messages, err := agent.PollMessagesAfter(context.Background(), session.ID, 0, types.MessageOutputOptions{})
	if err != nil {
		t.Fatalf("PollMessagesAfter() error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("PollMessagesAfter() returned %d messages, want 1", len(messages))
	}
	message := messages[0]
	if message.Content != "pong" {
		t.Fatalf("Content = %q, want pong", message.Content)
	}
	if message.Reasoning != "thought" {
		t.Fatalf("Reasoning = %q, want thought", message.Reasoning)
	}
	if !strings.Contains(message.Tools, "bash") {
		t.Fatalf("Tools = %q, want a bash tool call", message.Tools)
	}
	if !strings.Contains(message.Tools, "file.txt") {
		t.Fatalf("Tools = %q, want the tool result", message.Tools)
	}
	if message.Model.String() != "prov/m1" {
		t.Fatalf("Model = %s, want prov/m1", message.Model)
	}
}

func TestPollMessagesAfterHonoursCursorAndFilter(t *testing.T) {
	agent := newFakePi(t, func(f *fakePi, command string, fields map[string]any) {
		switch command {
		case "get_state":
			f.respond(fields["id"].(string), command, map[string]any{"sessionId": "ses_1"})
		case "prompt":
			f.respond(fields["id"].(string), command, map[string]any{"disposition": "started"})
			go func() {
				f.send(map[string]any{
					"type": "message_end",
					"message": map[string]any{
						"role":       "assistant",
						"stopReason": "stop",
						"content": []any{
							map[string]any{"type": "text", "text": "answer"},
							map[string]any{"type": "thinking", "thinking": "secret"},
						},
					},
				})
				f.send(map[string]any{"type": "agent_settled"})
			}()
		}
	})

	session, err := agent.CreateSession(context.Background(), types.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	handle, err := agent.Prompt(context.Background(), session.ID, "hi")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	select {
	case <-handle.Done():
	case err := <-handle.Err():
		t.Fatalf("Prompt() failed: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("Prompt() did not settle")
	}

	output := types.MessageOutputOptions{Include: []types.MessageContentKind{types.MessageContentAnswer}}
	messages, err := agent.PollMessagesAfter(context.Background(), session.ID, 0, output)
	if err != nil {
		t.Fatalf("PollMessagesAfter() error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("returned %d messages, want 1", len(messages))
	}
	if messages[0].Reasoning != "" {
		t.Fatalf("Reasoning = %q, want filtered out", messages[0].Reasoning)
	}

	// The cursor excludes settled turns.
	empty, err := agent.PollMessagesAfter(context.Background(), session.ID, 1e12, output)
	if err != nil {
		t.Fatalf("PollMessagesAfter() error = %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("returned %d messages past the cursor, want 0", len(empty))
	}
}

func TestPromptReportsProviderFailure(t *testing.T) {
	agent := newFakePi(t, func(f *fakePi, command string, fields map[string]any) {
		switch command {
		case "get_state":
			f.respond(fields["id"].(string), command, map[string]any{"sessionId": "ses_1"})
		case "prompt":
			f.respond(fields["id"].(string), command, map[string]any{"disposition": "started"})
			go func() {
				f.send(map[string]any{
					"type": "message_end",
					"message": map[string]any{
						"role":         "assistant",
						"stopReason":   "error",
						"errorMessage": "provider is down",
					},
				})
				f.send(map[string]any{"type": "agent_settled"})
			}()
		}
	})

	session, err := agent.CreateSession(context.Background(), types.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	handle, err := agent.Prompt(context.Background(), session.ID, "hi")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	select {
	case err := <-handle.Err():
		if err == nil || !strings.Contains(err.Error(), "provider is down") {
			t.Fatalf("error = %v, want the provider message", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Prompt() did not fail")
	}
}

func TestPromptCancellationAbortsRun(t *testing.T) {
	agent := newFakePi(t, func(f *fakePi, command string, fields map[string]any) {
		switch command {
		case "get_state":
			f.respond(fields["id"].(string), command, map[string]any{"sessionId": "ses_1"})
		case "prompt":
			// Accept the prompt but never settle.
			f.respond(fields["id"].(string), command, map[string]any{"disposition": "started"})
		case "abort":
			f.respond(fields["id"].(string), command, nil)
			go func() {
				f.send(map[string]any{"type": "agent_settled", "aborted": true})
			}()
		}
	})

	session, err := agent.CreateSession(context.Background(), types.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	handle, err := agent.Prompt(ctx, session.ID, "hang")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	cancel()
	select {
	case err := <-handle.Err():
		if err == nil {
			t.Fatal("expected an error after cancellation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Prompt() did not abort")
	}
}

func TestExtensionDialogsBecomeQuestions(t *testing.T) {
	agent := newFakePi(t, func(f *fakePi, command string, fields map[string]any) {
		switch command {
		case "get_state":
			f.respond(fields["id"].(string), command, map[string]any{"sessionId": "ses_1"})
		case "prompt":
			f.respond(fields["id"].(string), command, map[string]any{"disposition": "started"})
			go func() {
				f.send(map[string]any{
					"type":    "extension_ui_request",
					"id":      "ui-1",
					"method":  "select",
					"title":   "Allow dangerous command?",
					"options": []any{"Allow", "Block"},
				})
				f.send(map[string]any{
					"type":      "extension_ui_request",
					"id":        "ui-2",
					"method":    "setStatus",
					"statusKey": "ignored",
				})
			}()
		}
	})

	session, err := agent.CreateSession(context.Background(), types.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if _, err := agent.Prompt(context.Background(), session.ID, "run"); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	var requests []types.QuestionRequest
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		requests, err = agent.ListPendingQuestions(context.Background(), session.ID)
		if err == nil && len(requests) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(requests) != 1 {
		t.Fatalf("ListPendingQuestions() returned %d requests, want 1", len(requests))
	}
	if requests[0].Questions[0].Text != "Allow dangerous command?" {
		t.Fatalf("question text = %q", requests[0].Questions[0].Text)
	}
	if len(requests[0].Questions[0].Options) != 2 {
		t.Fatalf("options = %v, want 2", requests[0].Questions[0].Options)
	}

	if err := agent.ReplyQuestion(context.Background(), session.ID, requests[0].ID, [][]string{{"Block"}}); err != nil {
		t.Fatalf("ReplyQuestion() error = %v", err)
	}

	fake := fakeOf(agent)
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		last := fake.lastWrite()
		if last != nil && last["type"] == "extension_ui_response" {
			if last["value"] != "Block" {
				t.Fatalf("select answer = %v, want Block", last["value"])
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("extension_ui_response was never written")
}

func TestSelectOptionAcceptsIndex(t *testing.T) {
	options := []string{"Allow", "Block"}
	if resolved, ok := resolveSelectOption("2", options); !ok || resolved != "Block" {
		t.Fatalf("index selection = %q/%v, want Block", resolved, ok)
	}
	if _, ok := resolveSelectOption("nope", options); ok {
		t.Fatal("expected an unmatched answer to fail")
	}
}

func TestConfirmAnswerParsing(t *testing.T) {
	if !truthyAnswer("yes", nil) {
		t.Fatal("yes should confirm")
	}
	if truthyAnswer("no", nil) {
		t.Fatal("no should not confirm")
	}
	if !truthyAnswer("Allow", []string{"Allow", "Block"}) {
		t.Fatal("the first option should confirm")
	}
	if truthyAnswer("Block", []string{"Allow", "Block"}) {
		t.Fatal("the second option should not confirm")
	}
}

var _ = io.Discard

func TestSplitArgsNormalisesTemplateRenderings(t *testing.T) {
	cases := map[string][]string{
		"--mode rpc":             {"--mode", "rpc"},
		`["--mode", "rpc"]`:      {"--mode", "rpc"},
		`["--mode" "rpc"]`:       {"--mode", "rpc"},
		"[--mode rpc]":           {"--mode", "rpc"},
		"['--mode', 'rpc',]":     {"--mode", "rpc"},
		"--mode=rpc":             {"--mode=rpc"},
		"  --mode   rpc  ":       {"--mode", "rpc"},
		"--append-system-prompt": {"--append-system-prompt"},
	}
	for input, want := range cases {
		got := splitArgs([]string{input})
		if len(got) != len(want) {
			t.Fatalf("splitArgs(%q) = %v, want %v", input, got, want)
		}
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("splitArgs(%q) = %v, want %v", input, got, want)
			}
		}
	}
	if got := splitArgs(nil); len(got) != 0 {
		t.Fatalf("splitArgs(nil) = %v, want empty", got)
	}
}

func TestSplitArgsKeepsSeparateEntries(t *testing.T) {
	got := splitArgs([]string{"--mode", "rpc"})
	if len(got) != 2 || got[0] != "--mode" || got[1] != "rpc" {
		t.Fatalf("splitArgs() = %v, want [--mode rpc]", got)
	}
}

func TestEnsureRPCMode(t *testing.T) {
	cases := []struct {
		name  string
		input []string
		want  []string
	}{
		{"already rpc", []string{"--mode", "rpc"}, []string{"--mode", "rpc"}},
		{"empty", nil, []string{"--mode", "rpc"}},
		{"extra flags kept", []string{"--no-session"}, []string{"--mode", "rpc", "--no-session"}},
		// A configured text mode must not win: the bridge cannot read prose.
		{"wrong mode replaced", []string{"--mode", "text", "--no-session"}, []string{"--mode", "rpc", "--no-session"}},
		{"equals form replaced", []string{"--mode=json"}, []string{"--mode", "rpc"}},
		{"duplicate mode collapsed", []string{"--mode", "text", "--mode", "rpc"}, []string{"--mode", "rpc"}},
		{"dangling mode dropped", []string{"--mode"}, []string{"--mode", "rpc"}},
	}
	for _, testCase := range cases {
		got := ensureRPCMode(splitArgs(testCase.input))
		if strings.Join(got, " ") != strings.Join(testCase.want, " ") {
			t.Fatalf("%s: ensureRPCMode(%v) = %v, want %v", testCase.name, testCase.input, got, testCase.want)
		}
	}
}
