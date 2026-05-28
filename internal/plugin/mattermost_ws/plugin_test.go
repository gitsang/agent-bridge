package mattermost_ws

import (
	"testing"

	"github.com/gitsang/agent-bridge/internal/bridge"
)

func TestFormatReplyWithAgentContext(t *testing.T) {
	msg := &bridge.Message{
		Content: "hello",
		Agent: bridge.AgentContext{
			SessionID: "session-1",
			Title:     "Session Title",
			Model:     "gpt-test",
			Directory: "/tmp/project",
		},
	}

	got := formatReply(msg)
	for _, want := range []string{"hello", "Directory: /tmp/project", "Session: Session Title (session-1)", "Model: gpt-test"} {
		if !contains(got, want) {
			t.Errorf("formatReply() = %q, want to contain %q", got, want)
		}
	}
}

func TestFormatReplyWithoutAgentContext(t *testing.T) {
	msg := &bridge.Message{
		Content: "hello",
	}

	got := formatReply(msg)
	if got != "hello" {
		t.Errorf("formatReply() = %q, want %q", got, "hello")
	}
}

func TestFormatReplyWithPartialAgentContext(t *testing.T) {
	msg := &bridge.Message{
		Content: "hello",
		Agent: bridge.AgentContext{
			Model: "gpt-test",
		},
	}

	got := formatReply(msg)
	for _, want := range []string{"hello", "Model: gpt-test"} {
		if !contains(got, want) {
			t.Errorf("formatReply() = %q, want to contain %q", got, want)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
