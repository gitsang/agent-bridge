package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gitsang/agent-bridge/internal/agent"
	ocsdk "github.com/sst/opencode-sdk-go"
)

func TestListPendingPermissionsFiltersBySession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodGet; got != want {
			t.Fatalf("method = %s, want %s", got, want)
		}
		if got, want := r.URL.Path, "/permission"; got != want {
			t.Fatalf("path = %s, want %s", got, want)
		}
		if got := r.Header.Get("Authorization"); got == "" {
			t.Fatalf("Authorization header is empty")
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{
				"id": "perm-1",
				"sessionID": "s1",
				"permission": "edit",
				"patterns": ["src/*.go"],
				"metadata": {"file": "main.go"},
				"always": ["src/**"],
				"tool": {"messageID": "msg-1", "callID": "call-1"}
			},
			{
				"id": "perm-2",
				"sessionID": "s2",
				"permission": "bash"
			}
		]`))
	}))
	defer server.Close()

	client := NewClient(server.URL, WithAuthentication("user", "pass"))
	requests, err := client.ListPendingPermissions(context.Background(), "s1")
	if err != nil {
		t.Fatalf("ListPendingPermissions() error = %v", err)
	}
	if got, want := len(requests), 1; got != want {
		t.Fatalf("permission request count = %d, want %d", got, want)
	}
	got := requests[0]
	if got.ID != "perm-1" {
		t.Fatalf("permission id = %q, want perm-1", got.ID)
	}
	if got.SessionID != "s1" {
		t.Fatalf("permission session = %q, want s1", got.SessionID)
	}
	if got.Permission != "edit" {
		t.Fatalf("permission = %q, want edit", got.Permission)
	}
	if len(got.Patterns) != 1 || got.Patterns[0] != "src/*.go" {
		t.Fatalf("patterns = %#v, want src/*.go", got.Patterns)
	}
	if len(got.Always) != 1 || got.Always[0] != "src/**" {
		t.Fatalf("always = %#v, want src/**", got.Always)
	}
	if got.Metadata["file"] != "main.go" {
		t.Fatalf("metadata[file] = %#v, want main.go", got.Metadata["file"])
	}
	if got.Tool.MessageID != "msg-1" || got.Tool.CallID != "call-1" {
		t.Fatalf("tool = %#v, want message/call ids", got.Tool)
	}
}

func TestListPendingPermissionsMapsSDKPermissionShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodGet; got != want {
			t.Fatalf("method = %s, want %s", got, want)
		}
		if got, want := r.URL.Path, "/permission"; got != want {
			t.Fatalf("path = %s, want %s", got, want)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{
				"id": "perm-1",
				"sessionID": "s1",
				"title": "Edit src/main.go",
				"type": "edit",
				"pattern": "src/main.go",
				"metadata": {"file": "main.go"},
				"messageID": "msg-1",
				"callID": "call-1"
			},
			{
				"id": "perm-2",
				"sessionID": "s2",
				"title": "Run command",
				"type": "bash"
			}
		]`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	requests, err := client.ListPendingPermissions(context.Background(), "s1")
	if err != nil {
		t.Fatalf("ListPendingPermissions() error = %v", err)
	}
	if got, want := len(requests), 1; got != want {
		t.Fatalf("permission request count = %d, want %d", got, want)
	}
	got := requests[0]
	if got.Permission != "Edit src/main.go" {
		t.Fatalf("permission = %q, want Edit src/main.go", got.Permission)
	}
	if len(got.Patterns) != 1 || got.Patterns[0] != "src/main.go" {
		t.Fatalf("patterns = %#v, want src/main.go", got.Patterns)
	}
	if got.Tool.MessageID != "msg-1" || got.Tool.CallID != "call-1" {
		t.Fatalf("tool = %#v, want message/call ids", got.Tool)
	}
}

func TestReplyPermissionUsesSessionPermissionEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodPost; got != want {
			t.Fatalf("method = %s, want %s", got, want)
		}
		if got, want := r.URL.Path, "/session/s1/permissions/perm-1"; got != want {
			t.Fatalf("path = %s, want %s", got, want)
		}

		var body struct {
			Response string `json:"response"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if got, want := body.Response, "once"; got != want {
			t.Fatalf("response = %q, want %q", got, want)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`true`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	if err := client.ReplyPermission(context.Background(), "s1", "perm-1", agent.PermissionReplyOnce); err != nil {
		t.Fatalf("ReplyPermission() error = %v", err)
	}
}

func TestListPendingQuestionsFiltersBySession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodGet; got != want {
			t.Fatalf("method = %s, want %s", got, want)
		}
		if got, want := r.URL.Path, "/question"; got != want {
			t.Fatalf("path = %s, want %s", got, want)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{
				"id": "question-1",
				"sessionID": "s1",
				"questions": [
					{"text": "Environment?", "options": ["staging", "production"]},
					{"text": "Tags?", "options": ["smoke", "full"], "multiple": true}
				],
				"tool": {"messageID": "msg-1", "callID": "call-1"}
			},
			{
				"id": "question-2",
				"sessionID": "s2",
				"questions": [{"text": "Skip?"}]
			}
		]`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	requests, err := client.ListPendingQuestions(context.Background(), "s1")
	if err != nil {
		t.Fatalf("ListPendingQuestions() error = %v", err)
	}
	if got, want := len(requests), 1; got != want {
		t.Fatalf("question request count = %d, want %d", got, want)
	}
	got := requests[0]
	if got.ID != "question-1" {
		t.Fatalf("question id = %q, want question-1", got.ID)
	}
	if got.SessionID != "s1" {
		t.Fatalf("question session = %q, want s1", got.SessionID)
	}
	if got.Tool.MessageID != "msg-1" || got.Tool.CallID != "call-1" {
		t.Fatalf("tool = %#v, want message/call ids", got.Tool)
	}
	if got.Questions[0].Text != "Environment?" {
		t.Fatalf("question text = %q, want Environment?", got.Questions[0].Text)
	}
	if len(got.Questions[0].Options) != 2 || got.Questions[0].Options[1] != "production" {
		t.Fatalf("question options = %#v, want staging/production", got.Questions[0].Options)
	}
	if !got.Questions[1].Multiple {
		t.Fatalf("second question multiple = false, want true")
	}
}

func TestListPendingQuestionsMapsObjectOptionsAndQuestionField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodGet; got != want {
			t.Fatalf("method = %s, want %s", got, want)
		}
		if got, want := r.URL.Path, "/question"; got != want {
			t.Fatalf("path = %s, want %s", got, want)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{
				"id": "question-1",
				"sessionID": "s1",
				"questions": [
					{
						"question": "Proceed with deployment?",
						"header": "Deploy",
						"options": [
							{"label": "Yes", "description": "Deploy now"},
							{"label": "No", "description": "Stop deployment"}
						]
					}
				]
			}
		]`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	requests, err := client.ListPendingQuestions(context.Background(), "s1")
	if err != nil {
		t.Fatalf("ListPendingQuestions() error = %v", err)
	}
	if got, want := len(requests), 1; got != want {
		t.Fatalf("question request count = %d, want %d", got, want)
	}
	question := requests[0].Questions[0]
	if got, want := question.Text, "Proceed with deployment?"; got != want {
		t.Fatalf("question text = %q, want %q", got, want)
	}
	if len(question.Options) != 2 || question.Options[0] != "Yes" || question.Options[1] != "No" {
		t.Fatalf("question options = %#v, want Yes/No labels", question.Options)
	}
}

func TestReplyQuestionPostsAnswers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodPost; got != want {
			t.Fatalf("method = %s, want %s", got, want)
		}
		if got, want := r.URL.Path, "/question/question-1/reply"; got != want {
			t.Fatalf("path = %s, want %s", got, want)
		}

		var body struct {
			Answers [][]string `json:"answers"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if len(body.Answers) != 1 || len(body.Answers[0]) != 1 || body.Answers[0][0] != "production" {
			t.Fatalf("answers = %#v, want [[production]]", body.Answers)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`true`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	if err := client.ReplyQuestion(context.Background(), "s1", "question-1", [][]string{{"production"}}); err != nil {
		t.Fatalf("ReplyQuestion() error = %v", err)
	}
}

func TestRejectQuestionPostsRejectEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodPost; got != want {
			t.Fatalf("method = %s, want %s", got, want)
		}
		if got, want := r.URL.Path, "/question/question-1/reject"; got != want {
			t.Fatalf("path = %s, want %s", got, want)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`true`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	if err := client.RejectQuestion(context.Background(), "s1", "question-1"); err != nil {
		t.Fatalf("RejectQuestion() error = %v", err)
	}
}

func TestGetLatestAssistantMessageSkipsUnfinishedAssistant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodGet; got != want {
			t.Fatalf("method = %s, want %s", got, want)
		}
		if got, want := r.URL.Path, "/session/s1/message"; got != want {
			t.Fatalf("path = %s, want %s", got, want)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{
				"info": {
					"id": "old",
					"role": "assistant",
					"sessionID": "s1",
					"providerID": "anthropic",
					"modelID": "claude",
					"time": {"created": 1, "completed": 1}
				},
				"parts": []
			},
			{
				"info": {
					"id": "pending",
					"role": "assistant",
					"sessionID": "s1",
					"providerID": "anthropic",
					"modelID": "claude",
					"time": {"created": 2}
				},
				"parts": []
			}
		]`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	msg, err := client.GetLatestAssistantMessage(context.Background(), "s1")
	if err != nil {
		t.Fatalf("GetLatestAssistantMessage() error = %v", err)
	}
	if msg == nil {
		t.Fatalf("GetLatestAssistantMessage() = nil, want completed assistant")
	}
	if got, want := msg.ID, "old"; got != want {
		t.Fatalf("GetLatestAssistantMessage() id = %q, want %q", got, want)
	}
	if got, want := msg.CompletedAt, float64(1); got != want {
		t.Fatalf("GetLatestAssistantMessage() completed_at = %v, want %v", got, want)
	}
}

func TestPromptUsesConfiguredTimeout(t *testing.T) {
	release := make(chan struct{})
	aborted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodPost; got != want {
			t.Fatalf("method = %s, want %s", got, want)
		}

		switch r.URL.Path {
		case "/session/s1/message":
			select {
			case <-r.Context().Done():
			case <-release:
			}
		case "/session/s1/abort":
			aborted <- struct{}{}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`true`))
		default:
			t.Fatalf("path = %s, want prompt or abort endpoint", r.URL.Path)
		}
	}))
	defer func() {
		close(release)
		server.Close()
	}()

	client := NewClient(server.URL, WithTimeout(10*time.Millisecond))
	handle, err := client.Prompt(context.Background(), "s1", "hello")
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	select {
	case err := <-handle.Err():
		if err == nil {
			t.Fatalf("Prompt() timeout error = nil")
		}
		if !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("Prompt() timeout error = %q, want timed out", err.Error())
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for Prompt() timeout error")
	}

	select {
	case <-aborted:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for session abort after prompt timeout")
	}
}

func TestExtractReplyFiltersByMessageOutputOptions(t *testing.T) {
	parts := []ocsdk.Part{
		{Type: ocsdk.PartTypeText, Text: "final answer"},
		{Type: ocsdk.PartTypeReasoning, Text: "private chain"},
		{Type: ocsdk.PartTypeTool, Tool: "bash", State: ocsdk.ToolPartState{
			Input:  map[string]any{"cmd": "go test ./..."},
			Output: "ok",
		}},
		{Type: ocsdk.PartTypePatch, Files: []string{"main.go"}},
		{Type: ocsdk.PartTypeSnapshot, Snapshot: "state"},
	}

	tests := []struct {
		name        string
		output      agent.MessageOutputOptions
		wantContain []string
		wantSkip    []string
	}{
		{
			name:        "empty include outputs all mapped parts",
			output:      agent.MessageOutputOptions{},
			wantContain: []string{"final answer", "<thinking>", "<tool", "<patch>main.go</patch>", "<snapshot>state</snapshot>"},
		},
		{
			name: "answer only",
			output: agent.MessageOutputOptions{
				Include: []agent.MessageContentKind{agent.MessageContentAnswer},
			},
			wantContain: []string{"final answer"},
			wantSkip:    []string{"<thinking>", "<tool", "<patch>", "<snapshot>"},
		},
		{
			name: "parent categories include children",
			output: agent.MessageOutputOptions{
				Include: []agent.MessageContentKind{agent.MessageContentAction, agent.MessageContentArtifact},
			},
			wantContain: []string{"<tool", "<patch>main.go</patch>", "<snapshot>state</snapshot>"},
			wantSkip:    []string{"final answer", "<thinking>"},
		},
		{
			name: "unmatched category outputs nothing",
			output: agent.MessageOutputOptions{
				Include: []agent.MessageContentKind{agent.MessageContentDiagnostic},
			},
			wantSkip: []string{"final answer", "<thinking>", "<tool", "<patch>", "<snapshot>"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractReply(parts, tt.output)
			for _, want := range tt.wantContain {
				if !strings.Contains(got, want) {
					t.Fatalf("extractReply() = %q, want to contain %q", got, want)
				}
			}
			for _, skip := range tt.wantSkip {
				if strings.Contains(got, skip) {
					t.Fatalf("extractReply() = %q, want to skip %q", got, skip)
				}
			}
			if len(tt.wantContain) == 0 && got != "" {
				t.Fatalf("extractReply() = %q, want empty string", got)
			}
		})
	}
}
