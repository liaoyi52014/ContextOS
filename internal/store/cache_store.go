package store

import (
	"context"
	"time"

	"github.com/contextos/contextos/internal/types"
	"github.com/redis/go-redis/v9"
)

// Compile-time check that RedisCacheStore implements types.CacheStore.
var _ types.CacheStore = (*RedisCacheStore)(nil)
var _ types.PubSubCache = (*RedisCacheStore)(nil)

// RedisCacheStore implements types.CacheStore backed by Redis.
// It accepts redis.Cmdable so it works with standalone, sentinel, and cluster clients.
type RedisCacheStore struct {
	client redis.UniversalClient
}

// NewRedisCacheStore creates a new RedisCacheStore.
func NewRedisCacheStore(client redis.UniversalClient) *RedisCacheStore {
	return &RedisCacheStore{client: client}
}

// Get retrieves a value by key. Returns nil, nil if the key does not exist.
func (s *RedisCacheStore) Get(ctx context.Context, key string) ([]byte, error) {
	val, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}
	return val, nil
}

// Set stores a value with the given TTL.
func (s *RedisCacheStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return s.client.Set(ctx, key, value, ttl).Err()
}

// Delete removes a key.
func (s *RedisCacheStore) Delete(ctx context.Context, key string) error {
	return s.client.Del(ctx, key).Err()
}

// SetNX sets a value only if the key does not already exist. Returns true if the key was set.
func (s *RedisCacheStore) SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	return s.client.SetNX(ctx, key, value, ttl).Result()
}

// Publish sends a message to a Redis channel.
func (s *RedisCacheStore) Publish(ctx context.Context, channel string, value []byte) error {
	return s.client.Publish(ctx, channel, value).Err()
}

// Subscribe listens on a Redis channel and bridges messages into a byte stream.
func (s *RedisCacheStore) Subscribe(ctx context.Context, channel string) (<-chan []byte, func() error, error) {
	pubsub := s.client.Subscribe(ctx, channel)
	if _, err := pubsub.Receive(ctx); err != nil {
		_ = pubsub.Close()
		return nil, nil, err
	}
	out := make(chan []byte, 16)
	go func() {
		defer close(out)
		ch := pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				out <- []byte(msg.Payload)
			}
		}
	}()
	closeFn := func() error {
		return pubsub.Close()
	}
	return out, closeFn, nil
}
