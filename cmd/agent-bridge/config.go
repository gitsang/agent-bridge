package main

import (
	"time"

	"github.com/gitsang/agent-bridge/internal/agent"
)

type Config struct {
	Log struct {
		Handlers struct {
			Default string `mapstructure:"default"`
		} `mapstructure:"handlers"`
		Providers map[string][]LogConfig `mapstructure:"providers"`
	} `mapstructure:"log"`
	Platforms map[string]any `mapstructure:"platforms"`
	Agent   struct {
		Driver        string                     `default:"opencode" usage:"agent driver" mapstructure:"driver"`
		MessageOutput agent.MessageOutputOptions `default:"{}" usage:"agent message output options" mapstructure:"message_output"`
		Opencode      struct {
			BaseURL  string        `default:"http://127.0.0.1:4096" usage:"opencode agent server base URL" mapstructure:"base_url"`
			Username string        `default:"agent" usage:"opencode agent server username" mapstructure:"username"`
			Password string        `usage:"opencode agent server password" mapstructure:"password"`
			Timeout  time.Duration `default:"10m" usage:"opencode agent request timeout, default 10m, 0 means no timeout" mapstructure:"timeout"`
			DBPath   string        `usage:"opencode sqlite database path for listing all sessions" mapstructure:"db_path"`
		} `mapstructure:"opencode"`
		Codex struct {
			Command           string            `default:"codex" usage:"codex app-server command" mapstructure:"command"`
			Args              []string          `default:"[app-server,--listen,stdio://]" usage:"codex app-server command args" mapstructure:"args"`
			Env               map[string]string `usage:"codex app-server extra environment" mapstructure:"env"`
			Timeout           time.Duration     `default:"30m" usage:"codex turn timeout, default 30m, 0 means no timeout" mapstructure:"timeout"`
			InitializeTimeout time.Duration     `default:"15s" usage:"codex app-server initialize timeout" mapstructure:"initialize_timeout"`
		} `mapstructure:"codex"`
		Claude struct {
			Command string            `default:"claude" usage:"claude code command" mapstructure:"command"`
			Args    []string          `default:"[--bare,-p,--output-format,stream-json,--verbose]" usage:"claude code command args before prompt" mapstructure:"args"`
			Env     map[string]string `usage:"claude code extra environment" mapstructure:"env"`
			Timeout time.Duration     `default:"30m" usage:"claude code turn timeout, default 30m, 0 means no timeout" mapstructure:"timeout"`
		} `mapstructure:"claude"`
	} `mapstructure:"agent"`
	Conversation struct {
		Store struct {
			Type     string        `default:"memory" usage:"conversation store type: memory|file|sqlite" mapstructure:"type"`
			Path     string        `default:"data/conversation" usage:"conversation store path (file.json or sqlite.db)" mapstructure:"path"`
			TTL      time.Duration `default:"24h" usage:"conversation store ttl" mapstructure:"ttl"`
			MaxItems int           `default:"1024" usage:"conversation store max items" mapstructure:"max_items"`
		} `mapstructure:"store"`
		Message struct {
			IncludeUserIdentity bool `default:"false" usage:"include user identity in prompt" mapstructure:"include_user_identity"`
		} `mapstructure:"message"`
	} `mapstructure:"conversation"`
}
