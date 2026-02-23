package avoinspector

import (
	"sync"
	"time"
)

const (
	cacheMaxEntries    = 50
	cacheTTLSeconds    = 60
	cacheMaxAccesses   = 50
	cacheSweepInterval = 50
)

// cacheEntry stores a cached event spec along with metadata for eviction.
type cacheEntry struct {
	spec       *EventSpecResponse
	createdAt  time.Time
	accessCount int
	lastAccess time.Time
}

// eventSpecCache is a concurrency-safe LRU cache for event spec responses.
// It uses sync.RWMutex for concurrent read/write access.
type eventSpecCache struct {
	mu         sync.RWMutex
	entries    map[string]*cacheEntry
	opCount    int
}

// newEventSpecCache creates a new empty event spec cache.
func newEventSpecCache() *eventSpecCache {
	return &eventSpecCache{
		entries: make(map[string]*cacheEntry),
	}
}

// specCacheKey builds the cache key from apiKey, streamId, and eventName.
func specCacheKey(apiKey, streamId, eventName string) string {
	return apiKey + ":" + streamId + ":" + eventName
}

// get retrieves a cached entry. Returns the spec and true if found,
// or nil and false on cache miss. Evicts the entry if it has been
// accessed 50 or more times (per-entry eviction).
func (c *eventSpecCache) get(key string) (*EventSpecResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, exists := c.entries[key]
	if !exists {
		return nil, false
	}

	// TTL check
	if time.Since(entry.createdAt) > cacheTTLSeconds*time.Second {
		delete(c.entries, key)
		return nil, false
	}

	entry.accessCount++
	entry.lastAccess = time.Now()

	// Per-entry eviction: evict after 50 accesses
	if entry.accessCount >= cacheMaxAccesses {
		delete(c.entries, key)
		return nil, false
	}

	return entry.spec, true
}

// set adds or updates a cache entry. Triggers global sweep every 50 operations
// and performs LRU eviction when capacity exceeds 50.
func (c *eventSpecCache) set(key string, spec *EventSpecResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[key] = &cacheEntry{
		spec:       spec,
		createdAt:  time.Now(),
		accessCount: 0,
		lastAccess: time.Now(),
	}

	c.opCount++

	// Global sweep every cacheSweepInterval operations
	if c.opCount%cacheSweepInterval == 0 {
		c.sweepExpiredLocked()
	}

	// LRU eviction when over capacity
	for len(c.entries) > cacheMaxEntries {
		c.evictLRULocked()
	}
}

// flush removes all entries from the cache (e.g., on branchId change).
func (c *eventSpecCache) flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*cacheEntry)
	c.opCount = 0
}

// sweepExpiredLocked removes entries older than cacheTTLSeconds.
// Must be called with c.mu held.
func (c *eventSpecCache) sweepExpiredLocked() {
	now := time.Now()
	for k, entry := range c.entries {
		if now.Sub(entry.createdAt) > cacheTTLSeconds*time.Second {
			delete(c.entries, k)
		}
	}
}

// evictLRULocked removes the least recently accessed entry.
// Must be called with c.mu held.
func (c *eventSpecCache) evictLRULocked() {
	var oldestKey string
	var oldestTime time.Time
	first := true

	for k, entry := range c.entries {
		if first || entry.lastAccess.Before(oldestTime) {
			oldestKey = k
			oldestTime = entry.lastAccess
			first = false
		}
	}

	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}
