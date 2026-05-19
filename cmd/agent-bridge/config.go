package main

import (
	"time"

	"github.com/gitsang/agent-bridge/internal/agent"
)

type Config struct {
	Log struct {
		Handlers struct {
			Default string `json:"default" yaml:"default"`
		} `json:"handlers" yaml:"handlers"`
		Providers map[string][]LogConfig `json:"providers" yaml:"providers"`
	} `json:"log" yaml:"log"`
	Plugins map[string]any `json:"plugins" yaml:"plugins"`
	Agent   struct {
		Driver        string                     `default:"opencode" usage:"agent driver" json:"driver" yaml:"driver" mapstructure:"driver"`
		MessageOutput agent.MessageOutputOptions `default:"{}" usage:"agent message output options" json:"message_output" yaml:"message_output" mapstructure:"message_output"`
		Opencode      struct {
			BaseURL  string        `default:"http://127.0.0.1:4096" usage:"opencode agent server base URL" json:"base_url" yaml:"base_url"`
			Username string        `default:"agent" usage:"opencode agent server username" json:"username" yaml:"username"`
			Password string        `usage:"opencode agent server password" json:"password" yaml:"password"`
			Timeout  time.Duration `default:"10m" usage:"opencode agent request timeout, default 10m, 0 means no timeout" json:"timeout" yaml:"timeout"`
		} `json:"opencode" yaml:"opencode"`
	} `json:"agent" yaml:"agent"`
	ConversationStore struct {
		Type     string        `default:"memory" usage:"conversation store type: memory|file" json:"type" yaml:"type"`
		FilePath string        `default:"data/conversation_store.json" usage:"conversation store file path when type=file" json:"file_path" yaml:"file_path"`
		TTL      time.Duration `default:"24h" usage:"conversation store ttl" json:"ttl" yaml:"ttl"`
		MaxItems int           `default:"1024" usage:"conversation store max items" json:"max_items" yaml:"max_items"`
	} `json:"conversation_store" yaml:"conversation_store"`
}
