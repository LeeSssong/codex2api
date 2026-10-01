package cache

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/redis/go-redis/v9"
)

// RuntimeBatchWriter avoids a network round trip for every history identity.
// Writes are idempotent; callers may repeat the batch after a partial failure.
type RuntimeBatchWriter interface {
	SetRuntimeBatch(context.Context, string, map[string]json.RawMessage, time.Duration) error
}

func runtimeWriteKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key, value := range values {
		if key != "" && len(value) > 0 {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

func (tc *redisTokenCache) SetRuntimeBatch(ctx context.Context, namespace string, values map[string]json.RawMessage, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	keys := runtimeWriteKeys(values)
	for start := 0; start < len(keys); start += runtimeReadBatchSize {
		_, err := tc.client.Pipelined(ctx, func(pipe redis.Pipeliner) error {
			for _, key := range keys[start:min(start+runtimeReadBatchSize, len(keys))] {
				pipe.Set(ctx, runtimeValueKey(namespace, key), []byte(values[key]), ttl)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (tc *MemoryTokenCache) SetRuntimeBatch(ctx context.Context, namespace string, values map[string]json.RawMessage, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	expires := time.Now().Add(ttl)
	tc.mu.Lock()
	defer tc.mu.Unlock()
	for _, key := range runtimeWriteKeys(values) {
		if mapKey := runtimeMapKey(namespace, key); mapKey != "" {
			tc.runtime[mapKey] = memoryRuntimeEntry{value: append(json.RawMessage(nil), values[key]...), expiresAt: expires}
		}
	}
	return nil
}
