package store

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// Redis wraps go-redis and satisfies policy.RedisClient.
type Redis struct {
	client *redis.Client
}

// NewRedis parses the redis URL and returns a Redis adapter.
func NewRedis(redisURL string) *Redis {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		// Fall back to default localhost if parse fails
		opts = &redis.Options{Addr: "localhost:6379"}
	}
	return &Redis{client: redis.NewClient(opts)}
}

// Close closes the Redis connection.
func (r *Redis) Close() error {
	return r.client.Close()
}

// Publish sends a message to a Redis pub/sub channel.
func (r *Redis) Publish(ctx context.Context, channel string, message interface{}) error {
	return r.client.Publish(ctx, channel, fmt.Sprint(message)).Err()
}

// Subscribe subscribes to one or more channels and returns a message channel.
// Messages are delivered as strings; the caller should close via context cancellation.
func (r *Redis) Subscribe(ctx context.Context, channels ...string) (<-chan string, error) {
	sub := r.client.Subscribe(ctx, channels...)
	// Verify subscription succeeded
	if _, err := sub.Receive(ctx); err != nil {
		return nil, fmt.Errorf("redis subscribe: %w", err)
	}

	out := make(chan string, 64)
	go func() {
		defer close(out)
		ch := sub.Channel()
		for {
			select {
			case <-ctx.Done():
				sub.Close()
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				out <- msg.Payload
			}
		}
	}()
	return out, nil
}

// Client returns the underlying redis.Client for direct use (e.g., in agent).
func (r *Redis) Client() *redis.Client {
	return r.client
}
