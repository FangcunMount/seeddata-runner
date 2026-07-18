package seedapi

import (
	"container/list"
	"sync"
)

const publishedResourceCacheLimit = 256

type versionedCacheEntry[T any] struct {
	key   string
	value T
}

// versionedCache caches only immutable, explicitly versioned resources.
type versionedCache[T any] struct {
	mu      sync.Mutex
	limit   int
	entries map[string]*list.Element
	lru     *list.List
}

func newVersionedCache[T any](limit int) *versionedCache[T] {
	if limit <= 0 {
		limit = publishedResourceCacheLimit
	}
	return &versionedCache[T]{
		limit:   limit,
		entries: make(map[string]*list.Element, limit),
		lru:     list.New(),
	}
}

func (c *versionedCache[T]) Get(key string) (T, bool) {
	var zero T
	if c == nil || key == "" {
		return zero, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[key]
	if !ok {
		return zero, false
	}
	c.lru.MoveToFront(element)
	return element.Value.(versionedCacheEntry[T]).value, true
}

func (c *versionedCache[T]) Put(key string, value T) {
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		element.Value = versionedCacheEntry[T]{key: key, value: value}
		c.lru.MoveToFront(element)
		return
	}
	element := c.lru.PushFront(versionedCacheEntry[T]{key: key, value: value})
	c.entries[key] = element
	for c.lru.Len() > c.limit {
		oldest := c.lru.Back()
		entry := oldest.Value.(versionedCacheEntry[T])
		delete(c.entries, entry.key)
		c.lru.Remove(oldest)
	}
}
