package lru

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The least recently used entry goes first once the cache is full.
func TestCacheEvictsTheLeastRecentlyUsed(t *testing.T) {
	c := New[string, int](2)
	c.Add("a", 1)
	c.Add("b", 2)
	_, _ = c.Get("a")
	c.Add("c", 3)
	_, ok := c.Get("b")
	assert.False(t, ok, "b was used least recently")
	v, ok := c.Get("a")
	assert.True(t, ok)
	assert.Equal(t, 1, v)
	c.Add("a", 4)
	v, _ = c.Get("a")
	assert.Equal(t, 4, v, "adding again replaces")
	assert.Equal(t, 2, c.Len())
}
