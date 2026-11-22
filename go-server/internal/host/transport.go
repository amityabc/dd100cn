package host

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"sync"
)

// One request owns this resource at a time. Additional producers wait when the
// configured backlog fills. Ordinary single-producer traffic takes the fast path.
type serialQueue struct {
	mu      sync.Mutex
	waiting chan struct{}
}

func newSerialQueue(capacity int) serialQueue {
	return serialQueue{waiting: make(chan struct{}, capacity)}
}

func (q *serialQueue) Lock() {
	if q.mu.TryLock() {
		return
	}
	q.waiting <- struct{}{}
	q.mu.Lock()
	<-q.waiting
}

func (q *serialQueue) Unlock() { q.mu.Unlock() }

type chunkReader struct {
	io.Reader
	size int
}

func (r chunkReader) Read(p []byte) (int, error) {
	return r.Reader.Read(p[:min(len(p), r.size)])
}

type nativeConn struct {
	net.Conn
	reader   *bufio.Reader
	ctx      context.Context
	maxFrame int
	writer   serialQueue
}

func (c *nativeConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func (c *nativeConn) Write(frame []byte) (int, error) {
	if len(frame) > c.maxFrame {
		return 0, errors.New("outgoing frame exceeds configured limit")
	}
	c.writer.Lock()
	defer c.writer.Unlock()
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	if err := writeFull(c.Conn, frame); err != nil {
		return 0, err
	}
	return len(frame), nil
}
