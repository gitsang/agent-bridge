package main

import (
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/gitsang/agent-bridge/internal/agent/codex"
	coreplugin "github.com/gitsang/agent-bridge/internal/plugin"
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

func TestMainImportsMattermostPlugin(t *testing.T) {
	if _, ok := coreplugin.GetPluginFactory("mattermost"); !ok {
		t.Fatalf("mattermost plugin factory is not registered")
	}
}

func TestRedactLogValueRedactsSensitiveFields(t *testing.T) {
	input := map[string]any{
		"listen": ":24370",
		"token":  "mattermost-token",
		"nested": map[string]any{
			"password": "secret-password",
			"env": []any{
				map[string]any{"api_secret": "secret-value"},
			},
		},
	}

	got := redactLogValue(input)
	want := map[string]any{
		"listen": ":24370",
		"token":  "***",
		"nested": map[string]any{
			"password": "***",
			"env": []any{
				map[string]any{"api_secret": "***"},
			},
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("redactLogValue() = %#v, want %#v", got, want)
	}
}

func TestRedactLogValueHandlesNil(t *testing.T) {
	if got := redactLogValue(nil); got != nil {
		t.Fatalf("redactLogValue(nil) = %#v, want nil", got)
	}
}
