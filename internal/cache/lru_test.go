package cache

import (
	"sync"
	"testing"
	"time"
)

func TestLRUEvictsLeastRecentlyUsed(t *testing.T) {
	c := New[string, int](2, time.Minute)
	c.Set("a", 1)
	c.Set("b", 2)
	if _, ok := c.Get("a"); !ok { // a is now most recent
		t.Fatal("a missing")
	}
	c.Set("c", 3) // evicts b
	if _, ok := c.Get("b"); ok {
		t.Fatal("b should have been evicted")
	}
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatal("a evicted instead of b")
	}
	if s := c.Stats(); s.Evictions != 1 || s.Hits != 2 || s.Misses != 1 {
		t.Fatalf("stats %+v", s)
	}
}

func TestLRUExpiresEntries(t *testing.T) {
	now := time.Unix(0, 0)
	c := New[string, int](10, time.Second)
	c.now = func() time.Time { return now }
	c.Set("a", 1)
	c.SetTTL("b", 2, 5*time.Second)
	now = now.Add(2 * time.Second)
	if _, ok := c.Get("a"); ok {
		t.Fatal("a should be expired")
	}
	if _, ok := c.Get("b"); !ok {
		t.Fatal("b should still be fresh")
	}
	if c.Stats().Expirations != 1 || c.Len() != 1 {
		t.Fatalf("stats %+v len %d", c.Stats(), c.Len())
	}
	c.Set("b", 3) // overwrite refreshes value and ttl
	if v, _ := c.Get("b"); v != 3 {
		t.Fatal("overwrite failed")
	}
	c.Delete("b")
	c.Set("z", 1)
	c.Purge()
	if c.Len() != 0 {
		t.Fatal("purge failed")
	}
}

func TestLRUConcurrentUse(t *testing.T) {
	c := New[int, int](100, time.Minute)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				c.Set(i%150, i)
				c.Get(i % 150)
			}
		}()
	}
	wg.Wait()
	if c.Len() > 100 {
		t.Fatalf("len %d exceeds capacity", c.Len())
	}
}
