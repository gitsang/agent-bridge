package main

import (
	"log/slog"
	"testing"
	"time"

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
