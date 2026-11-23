package pvf

import (
	"container/list"
	"sync"
)

const (
	cacheBytes   = 32 << 20
	cacheEntries = 4096
	chunkLoads   = 2
)

type cachedChunk struct {
	key  int32
	data []byte
}

type chunkFlight struct {
	done chan struct{}
	data []byte
	err  error
}

type chunkCache struct {
	mu      sync.Mutex
	lru     list.List
	hits    map[int32]*list.Element
	flights map[int32]*chunkFlight
	permits chan struct{}
	size    int
}

func (c *chunkCache) init() {
	c.hits = make(map[int32]*list.Element)
	c.flights = make(map[int32]*chunkFlight)
	c.permits = make(chan struct{}, chunkLoads)
}

func (c *chunkCache) get(i int32, load func(int32) ([]byte, error)) ([]byte, error) {
	c.mu.Lock()
	if e := c.hits[i]; e != nil {
		c.lru.MoveToFront(e)
		data := e.Value.(cachedChunk).data
		c.mu.Unlock()
		return data, nil
	}
	if f := c.flights[i]; f != nil {
		c.mu.Unlock()
		<-f.done
		return f.data, f.err
	}
	f := &chunkFlight{done: make(chan struct{})}
	c.flights[i] = f
	c.mu.Unlock()

	c.permits <- struct{}{}
	f.data, f.err = load(i)
	<-c.permits

	c.mu.Lock()
	defer c.mu.Unlock()
	if f.err == nil && len(f.data) <= cacheBytes {
		for c.size+len(f.data) > cacheBytes || c.lru.Len() >= cacheEntries {
			last := c.lru.Back()
			evicted := last.Value.(cachedChunk)
			delete(c.hits, evicted.key)
			c.lru.Remove(last)
			c.size -= len(evicted.data)
		}
		c.hits[i] = c.lru.PushFront(cachedChunk{i, f.data})
		c.size += len(f.data)
	}
	delete(c.flights, i)
	close(f.done)
	return f.data, f.err
}

func (c *chunkCache) clear() {
	// The archive holds its exclusive lifetime lock and has drained readers.
	c.lru.Init()
	c.hits, c.flights, c.permits, c.size = nil, nil, nil, 0
}
