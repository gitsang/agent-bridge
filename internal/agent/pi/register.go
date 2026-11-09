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

			args := ensureRPCMode(splitArgs(stringSlice(configMap["args"])))

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

// splitArgs normalises configured args into separate argv entries. Templates
// render a list default either joined ("--mode rpc") or as list punctuation
// (`["--mode", "rpc"]`), and pi answers anything it cannot parse as a chat
// prompt instead of failing, so a mangled list silently turns into a paid,
// wrong-mode run rather than an error.
func splitArgs(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	joined := strings.TrimSpace(strings.Join(values, " "))
	if strings.HasPrefix(joined, "[") && strings.HasSuffix(joined, "]") {
		joined = strings.TrimSpace(joined[1 : len(joined)-1])
	}
	resolved := make([]string, 0, len(values))
	for _, field := range strings.Fields(joined) {
		// Strip the quoting and separating punctuation a rendered list carries.
		field = strings.Trim(field, `"'`)
		field = strings.TrimSuffix(field, ",")
		field = strings.Trim(field, `"'`)
		if field == "" {
			continue
		}
		resolved = append(resolved, field)
	}
	return resolved
}

// ensureRPCMode guarantees the child process runs in RPC mode: the rest of this
// package only speaks JSONL records, and pi in text mode answers our records as
// if they were user prompts — inventing a plausible "response" in prose instead
// of failing. Any configured --mode is therefore dropped and replaced.
func ensureRPCMode(args []string) []string {
	filtered := make([]string, 0, len(args)+2)
	for index := 0; index < len(args); index++ {
		field := args[index]
		if field == "--mode" || strings.HasPrefix(field, "--mode=") {
			// Skip the flag and the value it consumes, if any.
			if field == "--mode" && index+1 < len(args) {
				index++
			}
			continue
		}
		filtered = append(filtered, field)
	}
	return append([]string{"--mode", "rpc"}, filtered...)
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
