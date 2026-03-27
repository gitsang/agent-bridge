package main

import "time"

type Config struct {
	Log struct {
		Handlers struct {
			Default string `json:"default" yaml:"default"`
		} `json:"handlers" yaml:"handlers"`
		Providers map[string][]LogConfig `json:"providers" yaml:"providers"`
	} `json:"log" yaml:"log"`
	Plugins  map[string]any `json:"plugins" yaml:"plugins"`
	Opencode struct {
		BaseURL  string `default:"http://127.0.0.1:4096" usage:"opencode server base URL" json:"base_url" yaml:"base_url"`
		Username string `default:"opencode" usage:"opencode server username" json:"username" yaml:"username"`
		Password string `usage:"opencode server password" json:"password" yaml:"password"`
		Timeout  time.Duration `default:"10m" usage:"opencode request timeout" json:"timeout" yaml:"timeout"`
	} `json:"opencode" yaml:"opencode"`
}
