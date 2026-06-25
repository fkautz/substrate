// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package encodingbench

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// TestValkeyPerRecord measures the real in-memory cost per record (value plus the
// key and Valkey's per-key engine overhead) for each encoding, by loading the
// corpus into a live Valkey/Redis and reading used_memory. It is skipped unless
// ENCODINGBENCH_VALKEY_ADDR is set, e.g.:
//
//	valkey-server --port 6399 --save '' --appendonly no --daemonize yes
//	ENCODINGBENCH_VALKEY_ADDR=localhost:6399 go test ./internal/encodingbench/ -run TestValkeyPerRecord -v
//
// Use a throwaway instance: the test FLUSHALLs between encodings. The keys are
// identical across encodings (same corpus actor IDs), so the per-key overhead is
// constant and the differences reflect the value encoding alone.
func TestValkeyPerRecord(t *testing.T) {
	addr := os.Getenv("ENCODINGBENCH_VALKEY_ADDR")
	if addr == "" {
		t.Skip("set ENCODINGBENCH_VALKEY_ADDR=host:port (a throwaway Valkey) to measure real per-record memory")
	}
	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("cannot reach Valkey at %s: %v", addr, err)
	}

	corpus := Corpus()
	t.Logf("%-26s %12s  %10s", "encoding", "B/record", "value B")
	for _, c := range Codecs() {
		flushAndSettle(t, ctx, rdb)
		base := usedMemory(t, ctx, rdb)

		var sampleVal int
		pipe := rdb.Pipeline()
		for i, a := range corpus {
			b, err := c.Encode(a)
			if err != nil {
				t.Fatalf("%s: encode[%d]: %v", c.Name, i, err)
			}
			if i == len(corpus)/2 { // mid-corpus, outside the dictionary sample range
				sampleVal = len(b)
			}
			pipe.Set(ctx, "actor:"+a.GetActorId(), b, 0)
			if (i+1)%5000 == 0 {
				if _, err := pipe.Exec(ctx); err != nil {
					t.Fatalf("%s: pipeline exec: %v", c.Name, err)
				}
				pipe = rdb.Pipeline()
			}
		}
		if _, err := pipe.Exec(ctx); err != nil {
			t.Fatalf("%s: pipeline exec: %v", c.Name, err)
		}

		used := usedMemory(t, ctx, rdb)
		perRecord := float64(used-base) / float64(len(corpus))
		t.Logf("%-26s %12.1f  %10d", c.Name, perRecord, sampleVal)
	}
	flushAndSettle(t, ctx, rdb)
}

// flushAndSettle clears the keyspace and waits for used_memory to stop dropping,
// so the next baseline is not contaminated by the previous encoding's data.
func flushAndSettle(t *testing.T, ctx context.Context, rdb *redis.Client) {
	t.Helper()
	if err := rdb.FlushAll(ctx).Err(); err != nil {
		t.Fatalf("flushall: %v", err)
	}
	prev := int64(-1)
	for i := 0; i < 50; i++ {
		cur := usedMemory(t, ctx, rdb)
		if cur == prev {
			return
		}
		prev = cur
		time.Sleep(50 * time.Millisecond)
	}
}

func usedMemory(t *testing.T, ctx context.Context, rdb *redis.Client) int64 {
	t.Helper()
	info, err := rdb.Info(ctx, "memory").Result()
	if err != nil {
		t.Fatalf("info memory: %v", err)
	}
	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, "used_memory:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "used_memory:"))
			n, _ := strconv.ParseInt(v, 10, 64)
			return n
		}
	}
	t.Fatal("used_memory not found in INFO output")
	return 0
}
