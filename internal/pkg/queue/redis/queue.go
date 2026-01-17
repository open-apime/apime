package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/open-apime/apime/internal/pkg/queue"
	"github.com/redis/go-redis/v9"
)

type RedisQueue struct {
	client *redis.Client
	key    string
}

func NewQueue(client *redis.Client, key string) *RedisQueue {
	return &RedisQueue{
		client: client,
		key:    key,
	}
}

func (q *RedisQueue) Enqueue(ctx context.Context, event queue.Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("queue enqueue: marshal: %w", err)
	}

	if err := q.client.LPush(ctx, q.key, data).Err(); err != nil {
		return fmt.Errorf("queue enqueue: %w", err)
	}

	return nil
}

func (q *RedisQueue) Dequeue(ctx context.Context, timeout time.Duration) (*queue.Event, error) {
	result, err := q.client.BRPop(ctx, timeout, q.key).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil // Timeout
		}
		return nil, fmt.Errorf("queue dequeue: %w", err)
	}

	if len(result) < 2 {
		return nil, errors.New("queue dequeue: invalid result")
	}

	var event queue.Event
	if err := json.Unmarshal([]byte(result[1]), &event); err != nil {
		return nil, fmt.Errorf("queue dequeue: unmarshal: %w", err)
	}

	return &event, nil
}

func (q *RedisQueue) Size(ctx context.Context) (int64, error) {
	return q.client.LLen(ctx, q.key).Result()
}

func (q *RedisQueue) Close() error {
	// We don't close the redis client here as it might be shared
	return nil
}
