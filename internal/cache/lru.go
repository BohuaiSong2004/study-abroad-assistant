// Package cache provides a bounded, TTL-aware LRU cache.
package cache

import (
	"container/list"
	"sync"
	"sync/atomic"
	"time"
)

// Stats are cumulative counters.
type Stats struct {
	Hits, Misses, Evictions, Expirations uint64
}

// LRU is a fixed-capacity least-recently-used cache whose entries also
// expire after a TTL. It is safe for concurrent use.
type LRU[K comparable, V any] struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	ll       *list.List
	items    map[K]*list.Element
	now      func() time.Time

	hits, misses, evictions, expirations atomic.Uint64
}

type entry[K comparable, V any] struct {
	key     K
	val     V
	expires time.Time
}

// New creates an LRU holding at most capacity entries for ttl each.
func New[K comparable, V any](capacity int, ttl time.Duration) *LRU[K, V] {
	if capacity <= 0 {
		capacity = 1
	}
	return &LRU[K, V]{capacity: capacity, ttl: ttl, ll: list.New(), items: make(map[K]*list.Element), now: time.Now}
}

// Get returns the value and true on a fresh hit.
func (c *LRU[K, V]) Get(k K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero V
	el, ok := c.items[k]
	if !ok {
		c.misses.Add(1)
		return zero, false
	}
	e := el.Value.(*entry[K, V])
	if !c.now().Before(e.expires) {
		c.removeElement(el)
		c.expirations.Add(1)
		c.misses.Add(1)
		return zero, false
	}
	c.ll.MoveToFront(el)
	c.hits.Add(1)
	return e.val, true
}

// Set stores v under k with the cache's TTL.
func (c *LRU[K, V]) Set(k K, v V) { c.SetTTL(k, v, c.ttl) }

// SetTTL stores v under k with a specific TTL.
func (c *LRU[K, V]) SetTTL(k K, v V, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	exp := c.now().Add(ttl)
	if el, ok := c.items[k]; ok {
		e := el.Value.(*entry[K, V])
		e.val, e.expires = v, exp
		c.ll.MoveToFront(el)
		return
	}
	c.items[k] = c.ll.PushFront(&entry[K, V]{key: k, val: v, expires: exp})
	for c.ll.Len() > c.capacity {
		c.removeElement(c.ll.Back())
		c.evictions.Add(1)
	}
}

// Delete removes k.
func (c *LRU[K, V]) Delete(k K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[k]; ok {
		c.removeElement(el)
	}
}

// Purge removes every entry.
func (c *LRU[K, V]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	c.items = make(map[K]*list.Element)
}

// Len returns the number of entries, including not-yet-collected expired ones.
func (c *LRU[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

// Stats returns cumulative counters.
func (c *LRU[K, V]) Stats() Stats {
	return Stats{Hits: c.hits.Load(), Misses: c.misses.Load(), Evictions: c.evictions.Load(), Expirations: c.expirations.Load()}
}

func (c *LRU[K, V]) removeElement(el *list.Element) {
	c.ll.Remove(el)
	delete(c.items, el.Value.(*entry[K, V]).key)
}
