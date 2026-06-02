package codex

import (
	"fmt"
	"time"

	"github.com/gitsang/agent-bridge/internal/agent"
)

func init() {
	agent.Register(agent.Registration{
		Name: "codex",
		Factory: func(name string, configRaw any, infra agent.Infrastructure) (agent.Agent, error) {
			configMap, ok := configRaw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("codex: config must be a map")
			}

			command := "codex"
			if c, ok := configMap["command"].(string); ok && c != "" {
				command = c
			}

			args := []string{"app-server", "--listen", "stdio://"}
			if a, ok := configMap["args"].([]interface{}); ok {
				strArgs := make([]string, 0, len(a))
				for _, arg := range a {
					if s, ok := arg.(string); ok {
						strArgs = append(strArgs, s)
					}
				}
				if len(strArgs) > 0 {
					args = strArgs
				}
			}

			opts := []Option{
				WithLogger(infra.Logger),
				WithCommand(command, args...),
			}

			if env, ok := configMap["env"].(map[string]interface{}); ok {
				strEnv := make(map[string]string, len(env))
				for k, v := range env {
					if s, ok := v.(string); ok {
						strEnv[k] = s
					}
				}
				if len(strEnv) > 0 {
					opts = append(opts, WithEnv(strEnv))
				}
			}

			if t, ok := configMap["timeout"].(string); ok {
				if d, err := time.ParseDuration(t); err == nil {
					opts = append(opts, WithTimeout(d))
				}
			}

			if t, ok := configMap["initialize_timeout"].(string); ok {
				if d, err := time.ParseDuration(t); err == nil {
					opts = append(opts, WithInitializeTimeout(d))
				}
			}

			return NewClient(opts...), nil
		},
	})
}
