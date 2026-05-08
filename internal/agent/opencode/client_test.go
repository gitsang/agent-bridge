package opencode

import (
	"strings"
	"testing"

	"github.com/gitsang/agent-bridge/internal/agent"
	ocsdk "github.com/sst/opencode-sdk-go"
)

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
