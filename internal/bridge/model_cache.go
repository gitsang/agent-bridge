package bridge

import (
	"context"
	"sync"

	"github.com/gitsang/agent-bridge/internal/agent"
)

type modelCache struct {
	mu      sync.RWMutex
	entries map[agent.ModelRef]agent.ModelInfo
}

func newModelCache() *modelCache {
	return &modelCache{entries: map[agent.ModelRef]agent.ModelInfo{}}
}

func (c *modelCache) lookup(ref agent.ModelRef) (agent.ModelInfo, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	info, ok := c.entries[ref]
	return info, ok
}

func (c *modelCache) refresh(ctx context.Context, client agent.Client, directory string) error {
	models, err := client.ListModels(ctx, directory)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range models {
		c.entries[m.ModelRef] = m
	}
	return nil
}
