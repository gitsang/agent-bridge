package acp

import (
	"fmt"
	"strings"
	"time"

	"github.com/gitsang/agent-bridge/internal/agent"
	"github.com/gitsang/agent-bridge/internal/bridge"
)

func init() {
	agent.Register(agent.Registration{
		Name: "acp",
		Factory: func(name string, configRaw any, infra agent.Infrastructure) (bridge.Agent, error) {
			configMap, ok := configRaw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("acp: config must be a map")
			}

			command, _ := configMap["command"].(string)
			if strings.TrimSpace(command) == "" {
				return nil, fmt.Errorf("acp: command is required")
			}

			opts := []Option{
				WithLogger(infra.Logger),
				WithCommand(command, stringSlice(configMap["args"])...),
				WithEnv(stringMap(configMap["env"])),
			}

			if directory, _ := configMap["directory"].(string); strings.TrimSpace(directory) != "" {
				opts = append(opts, WithDirectory(directory))
			}
			if timeout, ok := configMap["timeout"].(string); ok {
				if d, err := time.ParseDuration(timeout); err == nil {
					opts = append(opts, WithTimeout(d))
				}
			}
			if timeout, ok := configMap["initialize_timeout"].(string); ok {
				if d, err := time.ParseDuration(timeout); err == nil {
					opts = append(opts, WithInitializeTimeout(d))
				}
			}

			return NewClient(opts...), nil
		},
	})
}

func stringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		if direct, ok := value.([]string); ok {
			return direct
		}
		return nil
	}
	resolved := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			resolved = append(resolved, text)
		}
	}
	return resolved
}

func stringMap(value any) map[string]string {
	items, ok := value.(map[string]any)
	if !ok {
		if direct, ok := value.(map[string]string); ok {
			return direct
		}
		return nil
	}
	resolved := make(map[string]string, len(items))
	for key, item := range items {
		if text, ok := item.(string); ok {
			resolved[key] = text
		}
	}
	return resolved
}
