package memory

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/open-apime/apime/internal/pkg/queue"
)

type MemoryQueue struct {
	events chan queue.Event
	mu     sync.RWMutex
	closed bool
}

func NewQueue(bufferSize int) *MemoryQueue {
	if bufferSize <= 0 {
		bufferSize = 1000 // default buffer
	}
	return &MemoryQueue{
		events: make(chan queue.Event, bufferSize),
	}
}

func (q *MemoryQueue) Enqueue(ctx context.Context, event queue.Event) error {
	q.mu.RLock()
	defer q.mu.RUnlock()

	if q.closed {
		return errors.New("queue is closed")
	}

	select {
	case q.events <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return errors.New("queue is full")
	}
}

func (q *MemoryQueue) Dequeue(ctx context.Context, timeout time.Duration) (*queue.Event, error) {
	select {
	case event, ok := <-q.events:
		if !ok {
			return nil, errors.New("queue is closed")
		}
		return &event, nil
	case <-time.After(timeout):
		return nil, nil // Timeout
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (q *MemoryQueue) Size(ctx context.Context) (int64, error) {
	return int64(len(q.events)), nil
}

func (q *MemoryQueue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if !q.closed {
		close(q.events)
		q.closed = true
	}
	return nil
}
