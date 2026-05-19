package opencode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gitsang/agent-bridge/internal/agent"
	ocsdk "github.com/sst/opencode-sdk-go"
)

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
