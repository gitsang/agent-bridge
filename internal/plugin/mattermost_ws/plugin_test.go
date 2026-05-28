package mattermost_ws

import (
	"testing"

	"github.com/gitsang/agent-bridge/internal/bridge"
)

func TestBuildAttachmentWithAgentContext(t *testing.T) {
	p := &Plugin{
		version:     "v1.0.0",
		agentDriver: "opencode",
	}

	msg := &bridge.Message{
		Content: "hello",
		Agent: bridge.AgentContext{
			SessionID: "session-1",
			Title:     "Session Title",
			Model:     "gpt-test",
			Directory: "/tmp/project",
		},
	}

	attachment := p.buildAttachment(msg)

	if attachment.Title != "Session Title" {
		t.Errorf("buildAttachment().Title = %q, want %q", attachment.Title, "Session Title")
	}
	if attachment.Text != "hello" {
		t.Errorf("buildAttachment().Text = %q, want %q", attachment.Text, "hello")
	}
	if attachment.Footer != "agent-bridge v1.0.0 (opencode)" {
		t.Errorf("buildAttachment().Footer = %q, want %q", attachment.Footer, "agent-bridge v1.0.0 (opencode)")
	}

	if len(attachment.Fields) != 3 {
		t.Fatalf("buildAttachment().Fields length = %d, want 3", len(attachment.Fields))
	}

	expectedFields := []struct {
		Title string
		Value string
		Short bool
	}{
		{Title: "Directory", Value: "/tmp/project", Short: true},
		{Title: "Model", Value: "gpt-test", Short: true},
		{Title: "Session", Value: "Session Title (session-1)", Short: false},
	}

	for i, expected := range expectedFields {
		field := attachment.Fields[i]
		if field.Title != expected.Title {
			t.Errorf("buildAttachment().Fields[%d].Title = %q, want %q", i, field.Title, expected.Title)
		}
		if field.Value != expected.Value {
			t.Errorf("buildAttachment().Fields[%d].Value = %q, want %q", i, field.Value, expected.Value)
		}
		if bool(field.Short) != expected.Short {
			t.Errorf("buildAttachment().Fields[%d].Short = %v, want %v", i, field.Short, expected.Short)
		}
	}
}

func TestBuildAttachmentWithoutAgentContext(t *testing.T) {
	p := &Plugin{
		version:     "dev",
		agentDriver: "claude",
	}

	msg := &bridge.Message{
		Content: "hello",
	}

	attachment := p.buildAttachment(msg)

	if attachment.Title != "" {
		t.Errorf("buildAttachment().Title = %q, want empty", attachment.Title)
	}
	if attachment.Text != "hello" {
		t.Errorf("buildAttachment().Text = %q, want %q", attachment.Text, "hello")
	}
	if attachment.Footer != "agent-bridge dev (claude)" {
		t.Errorf("buildAttachment().Footer = %q, want %q", attachment.Footer, "agent-bridge dev (claude)")
	}

	if len(attachment.Fields) != 3 {
		t.Fatalf("buildAttachment().Fields length = %d, want 3", len(attachment.Fields))
	}
}

func TestBuildAttachmentWithPartialAgentContext(t *testing.T) {
	p := &Plugin{
		version:     "v2.0.0",
		agentDriver: "codex",
	}

	msg := &bridge.Message{
		Content: "hello",
		Agent: bridge.AgentContext{
			Model: "gpt-test",
		},
	}

	attachment := p.buildAttachment(msg)

	if attachment.Title != "" {
		t.Errorf("buildAttachment().Title = %q, want empty", attachment.Title)
	}
	if attachment.Text != "hello" {
		t.Errorf("buildAttachment().Text = %q, want %q", attachment.Text, "hello")
	}
	if attachment.Footer != "agent-bridge v2.0.0 (codex)" {
		t.Errorf("buildAttachment().Footer = %q, want %q", attachment.Footer, "agent-bridge v2.0.0 (codex)")
	}

	if len(attachment.Fields) != 3 {
		t.Fatalf("buildAttachment().Fields length = %d, want 3", len(attachment.Fields))
	}

	if attachment.Fields[1].Value != "gpt-test" {
		t.Errorf("buildAttachment().Fields[1].Value = %q, want %q", attachment.Fields[1].Value, "gpt-test")
	}
}
