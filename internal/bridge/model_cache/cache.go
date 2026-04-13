package model_cache

import (
	"context"
	"sync"

	"github.com/gitsang/agent-bridge/internal/agent"
)

type Cache struct {
	mu      sync.RWMutex
	entries map[agent.ModelRef]agent.ModelInfo
}

func New() *Cache {
	return &Cache{entries: map[agent.ModelRef]agent.ModelInfo{}}
}

func (c *Cache) lookup(ref agent.ModelRef) (agent.ModelInfo, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	info, ok := c.entries[ref]
	return info, ok
}

func (c *Cache) refresh(ctx context.Context, client agent.Client, directory string) error {
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

func (c *Cache) Humanize(ctx context.Context, ref agent.ModelRef, client agent.Client, directory string) string {
	if ref.IsZero() {
		return ""
	}
	info, ok := c.lookup(ref)
	if !ok {
		_ = c.refresh(ctx, client, directory)
		info, ok = c.lookup(ref)
	}
	if ok && info.ModelName != "" {
		name := info.ModelName
		if info.ProviderName != "" {
			name = info.ProviderName + "/" + name
		}
		return name
	}
	return ref.String()
}
