package platform

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/gitsang/agent-bridge/internal/bridge"
)

type HandleFunc func(ctx context.Context, req *bridge.Message, reply bridge.ReplyFunc) error

type Platform interface {
	Name() string
	Serve(ctx context.Context, handle HandleFunc) error
	Send(ctx context.Context, req *bridge.Message) (*bridge.Message, error)
}

type Infrastructure struct {
	Logger      *slog.Logger
	Version     string
	AgentDriver string
}

type Construct func(name string, configRaw any, infra Infrastructure) (Platform, error)

type PlatformFactory struct {
	Name      string
	Construct Construct
}

var (
	registrationMu     sync.RWMutex
	platformFactoryMap = map[string]PlatformFactory{}
)

func Register(registration PlatformFactory) {
	if registration.Name == "" {
		panic("platform registration key is required")
	}
	if registration.Construct == nil {
		panic(fmt.Sprintf("platform %s build function is required", registration.Name))
	}

	registrationMu.Lock()
	defer registrationMu.Unlock()

	platformFactoryMap[registration.Name] = registration
}

func GetPlatformFactory(key string) (PlatformFactory, bool) {
	registrationMu.RLock()
	defer registrationMu.RUnlock()

	registration, ok := platformFactoryMap[key]
	return registration, ok
}
