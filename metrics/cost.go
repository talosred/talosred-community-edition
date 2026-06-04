package metrics

import (
	"log"
	"strings"
	"sync"
	"time"

	"github.com/talosred/ce/store"
)

const cacheTTL = 30 * time.Second

type pricingStore interface {
	ListPricing() ([]*store.ModelPricing, error)
}

type CostCalculator struct {
	store    pricingStore
	mu       sync.RWMutex
	cache    map[string]*store.ModelPricing
	cachedAt time.Time
}

func NewCostCalculator(s *store.Store) *CostCalculator {
	return &CostCalculator{store: s}
}

// Calculate returns cost in USD. Uses an in-memory cache refreshed every 30s.
// Falls back to prefix match for model variants (e.g. "gpt-4o-2024-08-06" → "gpt-4o").
func (c *CostCalculator) Calculate(model string, inputTok, outputTok int64) float64 {
	pricing := c.lookup(model)
	if pricing == nil {
		return 0
	}
	return (float64(inputTok)/1000)*pricing.InputPer1k + (float64(outputTok)/1000)*pricing.OutputPer1k
}

func (c *CostCalculator) lookup(model string) *store.ModelPricing {
	cache := c.getCache()

	// exact match
	if p, ok := cache[model]; ok {
		return p
	}
	// prefix match: "gpt-4o-2024-08-06" → "gpt-4o"
	for k, p := range cache {
		if strings.HasPrefix(model, k) {
			return p
		}
	}
	return nil
}

func (c *CostCalculator) getCache() map[string]*store.ModelPricing {
	c.mu.RLock()
	if time.Since(c.cachedAt) < cacheTTL && c.cache != nil {
		cache := c.cache
		c.mu.RUnlock()
		return cache
	}
	c.mu.RUnlock()

	return c.refreshCache()
}

func (c *CostCalculator) refreshCache() map[string]*store.ModelPricing {
	c.mu.Lock()
	defer c.mu.Unlock()

	// double-check after acquiring write lock
	if time.Since(c.cachedAt) < cacheTTL && c.cache != nil {
		return c.cache
	}

	rows, err := c.store.ListPricing()
	if err != nil {
		log.Printf("pricing cache refresh: %v", err)
		if c.cache != nil {
			return c.cache // serve stale rather than nothing
		}
		return map[string]*store.ModelPricing{}
	}

	m := make(map[string]*store.ModelPricing, len(rows))
	for _, p := range rows {
		m[p.Model] = p
	}
	c.cache = m
	c.cachedAt = time.Now()
	return m
}

// InvalidateCache forces the next Calculate to reload from the DB.
// Call after any pricing write in the dashboard.
func (c *CostCalculator) InvalidateCache() {
	c.mu.Lock()
	c.cachedAt = time.Time{}
	c.mu.Unlock()
}
