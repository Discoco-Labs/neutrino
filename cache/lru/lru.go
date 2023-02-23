package lru

import (
	"fmt"
	"sync"

	"github.com/lightninglabs/neutrino/cache"
)

// elementMap is an alias for a map from a generic interface to a list.Element.
type elementMap[K comparable, V any] map[K]V

// entry represents a (key,value) pair entry in the Cache. The Cache's list
// stores entries which let us get the cache key when an entry is evicted.
type entry[K comparable, V cache.Value] struct {
	key   K
	value V
}

// Cache provides a generic thread-safe lru cache that can be used for
// storing filters, blocks, etc.
type Cache[K comparable, V cache.Value] struct {
	// capacity represents how much this cache can hold. It could be number
	// of elements or a number of bytes, decided by the cache.Value's Size.
	capacity uint64

	// size represents the size of all the elements currently in the cache.
	size uint64

	// ll is a doubly linked list which keeps track of recency of used
	// elements by moving them to the front.
	ll *List[entry[K, V]]

	// cache is a generic cache which allows us to find an elements position
	// in the ll list from a given key.
	cache elementMap[K, *Element[entry[K, V]]]

	// mtx is used to make sure the Cache is thread-safe.
	mtx sync.RWMutex
}

// NewCache return a cache with specified capacity, the cache's size can't
// exceed that given capacity.
func NewCache[K comparable, V cache.Value](capacity uint64) *Cache[K, V] {
	return &Cache[K, V]{
		capacity: capacity,
		ll:       NewList[entry[K, V]](),
		cache:    make(map[K]*Element[entry[K, V]]),
	}
}

// evict will evict as many elements as necessary to make enough space for a new
// element with size needed to be inserted.
func (c *Cache[K, V]) evict(needed uint64) (bool, error) {
	if needed > c.capacity {
		return false, fmt.Errorf("can't evict %v elements in size, "+
			"since capacity is %v", needed, c.capacity)
	}

	evicted := false
	for c.capacity-c.size < needed {
		// We still need to evict some more elements.
		if c.ll.Len() == 0 {
			// We should never reach here.
			return false, fmt.Errorf("all elements got evicted, "+
				"yet still need to evict %v, likelihood of "+
				"error during size calculation",
				needed-(c.capacity-c.size))
		}

		// Find the least recently used item.
		if elr := c.ll.Back(); elr != nil {
			// Determine lru item's size.
			ce := elr.Value
			es, err := ce.value.Size()
			if err != nil {
				return false, fmt.Errorf("couldn't determine "+
					"size of existing cache value %v", err)
			}

			// Account for that element's removal in evicted and
			// cache size.
			c.size -= es

			// Remove the element from the cache.
			c.ll.Remove(elr)
			delete(c.cache, ce.key)
			evicted = true
		}
	}

	return evicted, nil
}

// Put inserts a given (key,value) pair into the cache, if the key already
// exists, it will replace value and update it to be most recent item in cache.
// The return value indicates whether items had to be evicted to make room for
// the new element.
func (c *Cache[K, V]) Put(key K, value V) (bool, error) {
	vs, err := value.Size()
	if err != nil {
		return false, fmt.Errorf("couldn't determine size of cache "+
			"value: %v", err)
	}

	if vs > c.capacity {
		return false, fmt.Errorf("can't insert entry of size %v into "+
			"cache with capacity %v", vs, c.capacity)
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()

	// If the element already exists, remove it and decrease cache's size.
	el, ok := c.cache[key]
	if ok {
		es, err := el.Value.value.Size()
		if err != nil {
			return false, fmt.Errorf("couldn't determine size of "+
				"existing cache value %v", err)
		}
		c.ll.Remove(el)
		c.size -= es
	}

	// Then we need to make sure we have enough space for the element, evict
	// elements if we need more space.
	evicted, err := c.evict(vs)
	if err != nil {
		return false, err
	}

	// We have made enough space in the cache, so just insert it.
	el = c.ll.PushFront(entry[K, V]{key, value})
	c.cache[key] = el
	c.size += vs

	return evicted, nil
}

// Get will return value for a given key, making the element the most recently
// accessed item in the process. Will return nil if the key isn't found.
func (c *Cache[K, V]) Get(key K) (V, error) {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	var defaultVal V

	el, ok := c.cache[key]
	if !ok {
		// Element not found in the cache.
		return defaultVal, cache.ErrElementNotFound
	}

	// When the cache needs to evict a element to make space for another
	// one, it starts eviction from the back, so by moving this element to
	// the front, it's eviction is delayed because it's recently accessed.
	c.ll.MoveToFront(el)
	return el.Value.value, nil
}

// Len returns number of elements in the cache.
func (c *Cache[K, V]) Len() int {
	c.mtx.RLock()
	defer c.mtx.RUnlock()

	return c.ll.Len()
}

// Delete removes an item from the cache.
func (c *Cache[K, V]) Delete(key K) {
	c.LoadAndDelete(key)
}

// LoadAndDelete queries an item and deletes it from the cache using the
// specified key.
func (c *Cache[K, V]) LoadAndDelete(key K) (V, bool) {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	var defaultVal V

	// Noop if the element doesn't exist.
	el, ok := c.cache[key]
	if !ok {
		return defaultVal, false
	}

	// Before we delete the element, we need to get its size.
	vs, err := el.Value.value.Size()
	if err != nil {
		return defaultVal, false
	}

	// Remove the element from the list.
	c.ll.Remove(el)

	// Delete the element from the cache and update the cache's size.
	delete(c.cache, key)
	c.size -= vs

	return el.Value.value, true
}

// Range iterates the cache.
//
// NOTE: the `visitor` is wrapped inside the Cache's mutex.
func (c *Cache[K, V]) Range(visitor func(K, V) bool) {
	c.mtx.RLock()
	defer c.mtx.RUnlock()

	for k, v := range c.cache {
		if !visitor(k, v.Value.value) {
			return
		}
	}
}
