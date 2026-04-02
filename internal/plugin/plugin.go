package plugin

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/gitsang/opencode-connect/internal/connect"
)

type HandleFunc func(ctx context.Context, req *connect.Message, reply connect.ReplyFunc) error

type Plugin interface {
	Name() string
	Serve(ctx context.Context, handle HandleFunc) error
	Send(ctx context.Context, req *connect.Message) (*connect.Message, error)
}

type Infrastructure struct {
	Logger *slog.Logger
}

type Construct func(name string, configRaw any, infra Infrastructure) (Plugin, error)

type PluginFactory struct {
	Name      string
	Construct Construct
}

var (
	registrationMu   sync.RWMutex
	pluginFactoryMap = map[string]PluginFactory{}
)

func Register(registration PluginFactory) {
	if registration.Name == "" {
		panic("plugin registration key is required")
	}
	if registration.Construct == nil {
		panic(fmt.Sprintf("plugin %s build function is required", registration.Name))
	}

	registrationMu.Lock()
	defer registrationMu.Unlock()

	pluginFactoryMap[registration.Name] = registration
}

func GetPluginFactory(key string) (PluginFactory, bool) {
	registrationMu.RLock()
	defer registrationMu.RUnlock()

	registration, ok := pluginFactoryMap[key]
	return registration, ok
}
