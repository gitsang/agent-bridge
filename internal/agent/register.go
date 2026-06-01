package agent

import (
	"fmt"
	"log/slog"
	"sync"
)

// Infrastructure contains shared dependencies for agent drivers
type Infrastructure struct {
	Logger *slog.Logger
}

// Factory creates an agent Client from configuration
type Factory func(name string, configRaw any, infra Infrastructure) (Client, error)

// Registration represents a registered agent driver
type Registration struct {
	Name    string
	Factory Factory
}

var (
	registrationMu    sync.RWMutex
	agentFactoryMap   = map[string]Registration{}
)

// Register adds an agent driver factory to the global registry
func Register(registration Registration) {
	if registration.Name == "" {
		panic("agent registration key is required")
	}
	if registration.Factory == nil {
		panic(fmt.Sprintf("agent %s factory function is required", registration.Name))
	}

	registrationMu.Lock()
	defer registrationMu.Unlock()

	agentFactoryMap[registration.Name] = registration
}

// GetAgentFactory retrieves a registered agent driver factory by name
func GetAgentFactory(name string) (Registration, bool) {
	registrationMu.RLock()
	defer registrationMu.RUnlock()

	registration, ok := agentFactoryMap[name]
	return registration, ok
}
