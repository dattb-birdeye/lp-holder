package service

import (
	"sync"
	"time"
)

// ttlCache is a small in-process cache with per-entry expiry, bounded both by
// entry count and by total weight (e.g. holder rows, a proxy for memory).
// When over a bound, expired entries are dropped first, then the oldest ones.
type ttlCache[V any] struct {
	mu         sync.Mutex
	maxEntries int
	maxWeight  int
	weight     int
	now        func() time.Time
	items      map[string]cacheEntry[V]
}

type cacheEntry[V any] struct {
	value   V
	created time.Time
	expires time.Time
	weight  int
}

func newTTLCache[V any](maxEntries, maxWeight int, now func() time.Time) *ttlCache[V] {
	return &ttlCache[V]{maxEntries: maxEntries, maxWeight: maxWeight, now: now, items: make(map[string]cacheEntry[V])}
}

func (c *ttlCache[V]) get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok {
		var zero V
		return zero, false
	}
	if !c.now().Before(e.expires) {
		c.removeLocked(key, e)
		var zero V
		return zero, false
	}
	return e.value, true
}

// set stores v for ttl. weight counts against maxWeight (use 1 if unused).
func (c *ttlCache[V]) set(key string, v V, ttl time.Duration, weight int) {
	if ttl <= 0 || c.maxEntries <= 0 || weight > c.maxWeight {
		return // caching disabled, or one value larger than the whole budget
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.items[key]; ok {
		c.removeLocked(key, old)
	}
	now := c.now()
	for len(c.items) >= c.maxEntries || c.weight+weight > c.maxWeight {
		if !c.evictOneLocked(now) {
			break
		}
	}
	c.items[key] = cacheEntry[V]{value: v, created: now, expires: now.Add(ttl), weight: weight}
	c.weight += weight
}

// evictOneLocked drops all expired entries, or else the oldest one.
func (c *ttlCache[V]) evictOneLocked(now time.Time) bool {
	oldestKey, found := "", false
	var oldest cacheEntry[V]
	evicted := false
	for k, e := range c.items {
		if !now.Before(e.expires) {
			c.removeLocked(k, e)
			evicted = true
			continue
		}
		if !found || e.created.Before(oldest.created) {
			oldestKey, oldest, found = k, e, true
		}
	}
	if evicted {
		return true
	}
	if found {
		c.removeLocked(oldestKey, oldest)
		return true
	}
	return false
}

func (c *ttlCache[V]) removeLocked(key string, e cacheEntry[V]) {
	delete(c.items, key)
	c.weight -= e.weight
}
