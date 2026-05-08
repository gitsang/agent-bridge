package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gitsang/agent-bridge/internal/agent"
	"github.com/gitsang/configer"
)

func TestConfigLoadsMessageOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`
agent:
  driver: opencode
  message_output:
    include:
      - answer
      - action
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfger := configer.New(configer.WithTemplate((*Config)(nil)))
	var c Config
	if err := cfger.Load(&c, path); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got, want := len(c.Agent.MessageOutput.Include), 2; got != want {
		t.Fatalf("include length = %d, want %d", got, want)
	}
	if got, want := c.Agent.MessageOutput.Include[0], agent.MessageContentAnswer; got != want {
		t.Fatalf("include[0] = %q, want %q", got, want)
	}
	if got, want := c.Agent.MessageOutput.Include[1], agent.MessageContentAction; got != want {
		t.Fatalf("include[1] = %q, want %q", got, want)
	}
}
