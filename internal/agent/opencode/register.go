package opencode

import (
	"fmt"
	"time"

	"github.com/gitsang/agent-bridge/internal/agent"
	"github.com/gitsang/agent-bridge/internal/bridge"
)

func init() {
	agent.Register(agent.Registration{
		Name: "opencode",
		Factory: func(name string, configRaw any, infra agent.Infrastructure) (bridge.Agent, error) {
			configMap, ok := configRaw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("opencode: config must be a map")
			}

			baseURL, _ := configMap["base_url"].(string)
			if baseURL == "" {
				baseURL = "http://127.0.0.1:4096"
			}

			username, _ := configMap["username"].(string)
			password, _ := configMap["password"].(string)

			timeout := 10 * time.Minute
			if t, ok := configMap["timeout"].(string); ok {
				if d, err := time.ParseDuration(t); err == nil {
					timeout = d
				}
			}

			dbPath, _ := configMap["db_path"].(string)

			opts := []Option{
				WithLogger(infra.Logger),
				WithTimeout(timeout),
			}
			if username != "" || password != "" {
				opts = append(opts, WithAuthentication(username, password))
			}
			if dbPath != "" {
				opts = append(opts, WithDBPath(dbPath))
			}

			return NewClient(baseURL, opts...), nil
		},
	})
}
