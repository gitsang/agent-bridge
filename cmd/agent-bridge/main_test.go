package main

import (
	"log/slog"
	"testing"
	"time"

	"github.com/gitsang/agent-bridge/internal/agent/claude"
	"github.com/gitsang/agent-bridge/internal/agent/codex"
)

func TestBuildAgentClientSupportsCodexDriver(t *testing.T) {
	var c Config
	c.Agent.Driver = "codex"
	c.Agent.Codex.Command = "codex"
	c.Agent.Codex.Args = []string{"app-server", "--listen", "stdio://"}
	c.Agent.Codex.Timeout = time.Minute
	c.Agent.Codex.InitializeTimeout = time.Second

	client, err := buildAgentClient(c, slog.Default())
	if err != nil {
		t.Fatalf("buildAgentClient() error = %v", err)
	}
	if _, ok := client.(*codex.Client); !ok {
		t.Fatalf("buildAgentClient() = %T, want *codex.Client", client)
	}
}

func TestBuildAgentClientSupportsClaudeDriver(t *testing.T) {
	var c Config
	c.Agent.Driver = "claude"
	c.Agent.Claude.Command = "claude"
	c.Agent.Claude.Args = []string{"--bare", "-p", "--output-format", "stream-json", "--verbose"}
	c.Agent.Claude.Timeout = time.Minute

	client, err := buildAgentClient(c, slog.Default())
	if err != nil {
		t.Fatalf("buildAgentClient() error = %v", err)
	}
	if _, ok := client.(*claude.Client); !ok {
		t.Fatalf("buildAgentClient() = %T, want *claude.Client", client)
	}
}
