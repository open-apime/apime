package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type Queue struct {
	client *Client
	key    string
}

type Event struct {
	ID         string                 `json:"id"`
	InstanceID string                 `json:"instanceId"`
	Type       string                 `json:"type"`
	Payload    map[string]interface{} `json:"payload"`
	CreatedAt  time.Time              `json:"createdAt"`
}

func (q *Queue) Enqueue(ctx context.Context, event Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("queue enqueue: marshal: %w", err)
	}

	if err := q.client.rdb.LPush(ctx, q.key, data).Err(); err != nil {
		return fmt.Errorf("queue enqueue: %w", err)
	}

	return nil
}

func (q *Queue) Dequeue(ctx context.Context, timeout time.Duration) (*Event, error) {
	result, err := q.client.rdb.BRPop(ctx, timeout, q.key).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil // Timeout, sem eventos
		}
		return nil, fmt.Errorf("queue dequeue: %w", err)
	}

	if len(result) < 2 {
		return nil, fmt.Errorf("queue dequeue: resultado inválido")
	}

	var event Event
	if err := json.Unmarshal([]byte(result[1]), &event); err != nil {
		return nil, fmt.Errorf("queue dequeue: unmarshal: %w", err)
	}

	return &event, nil
}

func (q *Queue) Size(ctx context.Context) (int64, error) {
	return q.client.rdb.LLen(ctx, q.key).Result()
}

func NewQueue(client *Client, key string) *Queue {
	return &Queue{
		client: client,
		key:    key,
	}
}
