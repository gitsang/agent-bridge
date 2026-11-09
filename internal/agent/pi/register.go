package pi

import (
	"fmt"
	"strings"
	"time"

	"github.com/gitsang/agent-bridge/internal/agent"
	"github.com/gitsang/agent-bridge/internal/bridge"
)

func init() {
	agent.Register(agent.Registration{
		Name: "pi",
		Factory: func(name string, configRaw any, infra agent.Infrastructure) (bridge.Agent, error) {
			configMap, ok := configRaw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("pi: config must be a map")
			}

			command, _ := configMap["command"].(string)
			if strings.TrimSpace(command) == "" {
				command = "pi"
			}

			args := []string{"--mode", "rpc"}
			if resolved := stringSlice(configMap["args"]); len(resolved) > 0 {
				args = resolved
			}

			opts := []Option{
				WithLogger(infra.Logger),
				WithCommand(command, args...),
				WithEnv(stringMap(configMap["env"])),
			}
			if directory, _ := configMap["directory"].(string); strings.TrimSpace(directory) != "" {
				opts = append(opts, WithDirectory(directory))
			}
			if sessionDir, _ := configMap["session_dir"].(string); strings.TrimSpace(sessionDir) != "" {
				opts = append(opts, WithSessionDir(sessionDir))
			}
			if timeout, ok := configMap["timeout"].(string); ok {
				if d, err := time.ParseDuration(timeout); err == nil {
					opts = append(opts, WithTimeout(d))
				}
			}

			return NewClient(opts...), nil
		},
	})
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		resolved := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				resolved = append(resolved, text)
			}
		}
		return resolved
	}
	return nil
}

func stringMap(value any) map[string]string {
	switch typed := value.(type) {
	case map[string]string:
		return typed
	case map[string]any:
		resolved := make(map[string]string, len(typed))
		for key, item := range typed {
			if text, ok := item.(string); ok {
				resolved[key] = text
			}
		}
		return resolved
	}
	return nil
}
