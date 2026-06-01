package model_cache

import (
	"context"
	"sync"

	"github.com/gitsang/agent-bridge/internal/types"
)

type AgentModelLister interface {
	ListModels(ctx context.Context, directory string) ([]types.ModelInfo, error)
}

type Cache struct {
	mu      sync.RWMutex
	entries map[types.ModelRef]types.ModelInfo
}

func New() *Cache {
	return &Cache{entries: map[types.ModelRef]types.ModelInfo{}}
}

func (c *Cache) lookup(ref types.ModelRef) (types.ModelInfo, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	info, ok := c.entries[ref]
	return info, ok
}

func (c *Cache) refresh(ctx context.Context, client AgentModelLister, directory string) error {
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

func (c *Cache) Humanize(ctx context.Context, ref types.ModelRef, client AgentModelLister, directory string) string {
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
