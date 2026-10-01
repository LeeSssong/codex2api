package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestRuntimeBatchWritesRemainIsolatedAndCancelable(t *testing.T) {
	for _, driver := range []string{"memory", "redis"} {
		t.Run(driver, func(t *testing.T) {
			var backend TokenCache
			if driver == "redis" {
				backend = newBatchTestRedis(t)
			} else {
				backend = NewMemory(1)
				t.Cleanup(func() { _ = backend.Close() })
			}
			ns := fmt.Sprintf("batch-write-test-%d", time.Now().UnixNano())
			values := map[string]json.RawMessage{}
			keys := []string{}
			for i := range runtimeReadBatchSize + 3 {
				key := fmt.Sprintf("key:%d", i)
				keys = append(keys, key)
				values[key] = json.RawMessage(`"codex"`)
			}
			ctx := context.Background()
			t.Cleanup(func() {
				for _, key := range keys {
					_ = backend.DeleteRuntime(ctx, ns, key)
				}
			})
			writer := backend.(RuntimeBatchWriter)
			if err := writer.SetRuntimeBatch(ctx, ns, values, time.Hour); err != nil {
				t.Fatal(err)
			}
			values[keys[0]][0] = 'x'
			result, err := backend.(RuntimeBatchReader).GetRuntimeBatch(ctx, ns, keys)
			if err != nil || len(result) != len(keys) || string(result[keys[0]]) != `"codex"` {
				t.Fatalf("batch truncated or payload aliased: %d %v", len(result), err)
			}
			if _, found, err := backend.GetRuntime(ctx, ns+"other", keys[0]); found || err != nil {
				t.Fatal("namespace leaked")
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if err := writer.SetRuntimeBatch(canceled, ns, map[string]json.RawMessage{keys[0]: json.RawMessage(`"basispoints"`)}, time.Hour); err == nil {
				t.Fatal("canceled batch was accepted")
			}
			if raw, _, _ := backend.GetRuntime(ctx, ns, keys[0]); string(raw) != `"codex"` {
				t.Fatal("canceled batch replaced provenance")
			}
		})
	}
}
