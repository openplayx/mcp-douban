package douban

import (
	"sync"
	"time"
)

// cache is a small TTL cache of raw upstream bodies with request
// coalescing: concurrent misses for the same key share one upstream call.
type cache struct {
	mu       sync.Mutex
	max      int
	entries  map[string]cacheEntry
	inflight map[string]*call
	now      func() time.Time
}

type cacheEntry struct {
	body    []byte
	expires time.Time
}

type call struct {
	done chan struct{}
	body []byte
	err  error
}

func newCache(max int) *cache {
	return &cache{max: max, entries: map[string]cacheEntry{}, inflight: map[string]*call{}, now: time.Now}
}

// do returns the cached body for key, or runs fetch once (shared by
// concurrent callers) and caches a successful result for ttl.
func (c *cache) do(key string, ttl time.Duration, fetch func() ([]byte, error)) ([]byte, bool, error) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && c.now().Before(e.expires) {
		c.mu.Unlock()
		return e.body, true, nil
	}
	if cl, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		<-cl.done
		return cl.body, false, cl.err
	}
	cl := &call{done: make(chan struct{})}
	c.inflight[key] = cl
	c.mu.Unlock()

	cl.body, cl.err = fetch()

	c.mu.Lock()
	delete(c.inflight, key)
	if cl.err == nil && ttl > 0 && c.max > 0 {
		c.evictLocked()
		c.entries[key] = cacheEntry{body: cl.body, expires: c.now().Add(ttl)}
	}
	c.mu.Unlock()
	close(cl.done)
	return cl.body, false, cl.err
}

// evictLocked makes room for one entry: expired entries go first, then
// arbitrary ones (map order) until below the cap.
func (c *cache) evictLocked() {
	if len(c.entries) < c.max {
		return
	}
	now := c.now()
	for k, e := range c.entries {
		if !now.Before(e.expires) {
			delete(c.entries, k)
		}
	}
	for k := range c.entries {
		if len(c.entries) < c.max {
			break
		}
		delete(c.entries, k)
	}
}

func (c *cache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
