package claude

import (
	"fmt"
	"time"

	"github.com/gitsang/agent-bridge/internal/agent"
	"github.com/gitsang/agent-bridge/internal/bridge"
)

func init() {
	agent.Register(agent.Registration{
		Name: "claude",
		Factory: func(name string, configRaw any, infra agent.Infrastructure) (bridge.Agent, error) {
			configMap, ok := configRaw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("claude: config must be a map")
			}

			command := "claude"
			if c, ok := configMap["command"].(string); ok && c != "" {
				command = c
			}

			args := []string{"--bare", "-p", "--output-format", "stream-json", "--verbose"}
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

			return NewClient(opts...), nil
		},
	})
}
