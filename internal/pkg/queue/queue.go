package queue

import (
	"context"
	"time"
)

type Event struct {
	ID         string                 `json:"id"`
	InstanceID string                 `json:"instanceId"`
	Type       string                 `json:"type"`
	Payload    map[string]interface{} `json:"payload"`
	CreatedAt  time.Time              `json:"createdAt"`
}

type Queue interface {
	Enqueue(ctx context.Context, event Event) error
	Dequeue(ctx context.Context, timeout time.Duration) (*Event, error)
	Size(ctx context.Context) (int64, error)
	Close() error
}
