// Package lru is a size-bounded, least-recently-used cache, safe for
// concurrent use: the bound on every cache keyed by what a caller names.
package lru

import (
	"container/list"
	"sync"
)

// Cache holds at most its capacity of entries.
type Cache[K comparable, V any] struct {
	mu    sync.Mutex
	cap   int
	order *list.List
	items map[K]*list.Element
}

type entry[K comparable, V any] struct {
	key   K
	value V
}

// New builds a cache of capacity entries (at least one).
func New[K comparable, V any](capacity int) *Cache[K, V] {
	if capacity < 1 {
		capacity = 1
	}
	return &Cache[K, V]{cap: capacity, order: list.New(), items: map[K]*list.Element{}}
}

// Get returns the value of key and marks it used.
func (c *Cache[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)
		return el.Value.(*entry[K, V]).value, true
	}
	var zero V
	return zero, false
}

// Add sets key to value, evicting the least recently used entry when full.
func (c *Cache[K, V]) Add(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		el.Value.(*entry[K, V]).value = value
		c.order.MoveToFront(el)
		return
	}
	c.items[key] = c.order.PushFront(&entry[K, V]{key: key, value: value})
	if c.order.Len() > c.cap {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*entry[K, V]).key)
	}
}

// Len is the number of entries.
func (c *Cache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
