package opencode2

import (
	"fmt"
	"time"

	"github.com/gitsang/agent-bridge/internal/agent"
	"github.com/gitsang/agent-bridge/internal/bridge"
)

func init() {
	agent.Register(agent.Registration{
		Name: "opencode2",
		Factory: func(name string, configRaw any, infra agent.Infrastructure) (bridge.Agent, error) {
			configMap, ok := configRaw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("opencode2: config must be a map")
			}

			baseURL, _ := configMap["base_url"].(string)
			if baseURL == "" {
				baseURL = "http://127.0.0.1:4096"
			}
			username, _ := configMap["username"].(string)
			password, _ := configMap["password"].(string)

			opts := []Option{WithLogger(infra.Logger)}
			if username != "" || password != "" {
				opts = append(opts, WithAuthentication(username, password))
			}
			if timeout, ok := configMap["timeout"].(string); ok {
				if d, err := time.ParseDuration(timeout); err == nil {
					opts = append(opts, WithTimeout(d))
				}
			}
			if poll, ok := configMap["poll_interval"].(string); ok {
				if d, err := time.ParseDuration(poll); err == nil {
					opts = append(opts, WithPollInterval(d))
				}
			}

			return NewClient(baseURL, opts...), nil
		},
	})
}
