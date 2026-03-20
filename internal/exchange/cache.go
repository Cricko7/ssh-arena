package exchange

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

type Cache interface {
	PutSnapshot(ctx context.Context, symbol string, snapshotJSON string) error
	Publish(ctx context.Context, channel string, payloadJSON string) error
}

type RedisCache struct {
	client *redis.Client
}

func NewRedisCache(client *redis.Client) *RedisCache {
	return &RedisCache{client: client}
}

func (c *RedisCache) PutSnapshot(ctx context.Context, symbol string, snapshotJSON string) error {
	return c.client.Set(ctx, snapshotKey(symbol), snapshotJSON, 0).Err()
}

func (c *RedisCache) Publish(ctx context.Context, channel string, payloadJSON string) error {
	return c.client.Publish(ctx, channel, payloadJSON).Err()
}

func snapshotKey(symbol string) string {
	return fmt.Sprintf("arena:orderbook:%s", symbol)
}
