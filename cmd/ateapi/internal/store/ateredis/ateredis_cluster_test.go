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

//go:build integration

// This file holds round-trip tests that must run against a real Valkey/Redis
// cluster. miniredis (used by the unit tests) cannot execute cluster commands,
// so these are gated behind the `integration` build tag and skipped unless
// ATE_TEST_REDIS_ADDR points at a reachable cluster node.
//
// Run with, e.g.:
//
//	ATE_TEST_REDIS_ADDR=127.0.0.1:6379 go test -tags integration \
//	    ./cmd/ateapi/internal/store/ateredis/
package ateredis

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"

	"github.com/agent-substrate/substrate/pkg/proto/ateapipb"
)

// testKeyPatterns enumerates every key shape these tests create. Cleanup
// deletes exactly these rather than issuing a FLUSHALL, so the suite never runs
// a destructive flush against the target cluster. Keep this in sync with the
// keys the tests write; a missed pattern leaks a key, which the empty-keyspace
// guard in setupClusterTest then surfaces loudly on the next run.
var testKeyPatterns = []string{"actor:*", "worker:*", "test-lock"}

// setupClusterTest connects to a real cluster and ensures each test starts and
// ends from a clean keyspace.
//
// These tests mutate shared cluster state. To prevent a mistyped
// ATE_TEST_REDIS_ADDR from wiping a real server, setup refuses to run unless the
// target keyspace is already empty, and cleanup deletes only the keys these
// tests create (never FLUSHALL). Point ATE_TEST_REDIS_ADDR at a dedicated,
// disposable test cluster.
//
// Do NOT add t.Parallel() to these tests: they share one keyspace, and the
// empty-keyspace guard plus per-test key deletion assume serial execution.
// Running them in parallel would make setups and cleanups race and wipe each
// other's data.
func setupClusterTest(t *testing.T) (*Persistence, context.Context) {
	t.Helper()
	addr := os.Getenv("ATE_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("ATE_TEST_REDIS_ADDR not set; skipping real-cluster round-trip test")
	}
	ctx := context.Background()
	rdb := redis.NewClusterClient(&redis.ClusterOptions{Addrs: []string{addr}})
	t.Cleanup(func() { _ = rdb.Close() })
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("could not reach cluster at %s: %v", addr, err)
	}

	// Safety guard: never mutate a keyspace that already holds data. This makes
	// "oops, that was a real server" fail loudly here instead of destroying
	// data. Count keys across every master before touching anything.
	var keyCount int64
	if err := rdb.ForEachMaster(ctx, func(ctx context.Context, master *redis.Client) error {
		n, err := master.DBSize(ctx).Result()
		if err != nil {
			return err
		}
		keyCount += n
		return nil
	}); err != nil {
		t.Fatalf("could not check keyspace size at %s: %v", addr, err)
	}
	if keyCount != 0 {
		t.Fatalf("refusing to run destructive test: cluster at %s holds %d keys. "+
			"Point ATE_TEST_REDIS_ADDR at a dedicated, empty test cluster.", addr, keyCount)
	}

	s := &Persistence{rdb: rdb}
	// Surgical cleanup: delete only the keys this suite creates. We verified the
	// keyspace was empty at start, so this leaves it empty (and reusable) for the
	// next test without ever issuing a FLUSHALL.
	t.Cleanup(func() { clearTestKeys(ctx, t, rdb) })
	return s, ctx
}

// clearTestKeys deletes every key matching testKeyPatterns across all masters.
func clearTestKeys(ctx context.Context, t *testing.T, rdb *redis.ClusterClient) {
	t.Helper()
	err := rdb.ForEachMaster(ctx, func(ctx context.Context, master *redis.Client) error {
		for _, pattern := range testKeyPatterns {
			iter := master.Scan(ctx, 0, pattern, 0).Iterator()
			var keys []string
			for iter.Next(ctx) {
				keys = append(keys, iter.Val())
			}
			if err := iter.Err(); err != nil {
				return err
			}
			for _, k := range keys {
				if err := master.Del(ctx, k).Err(); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		// Cleanup failure isn't a test failure on its own, but leaked keys would
		// trip the empty-keyspace guard on the next run, so make it visible.
		t.Logf("warning: clearTestKeys failed, keyspace may not be clean: %v", err)
	}
}

// assertKeysAbsent fails the test if any of the given keys already exist. This
// is belt-and-suspenders on top of the empty-keyspace guard: it makes each test
// explicit about the keys it owns and guarantees we never stomp a pre-existing
// key. It matters most for the corrupt-value subtests, which seed keys with a
// raw SET (which overwrites) rather than the store's SetNX (which does not).
func assertKeysAbsent(ctx context.Context, t *testing.T, s *Persistence, keys ...string) {
	t.Helper()
	for _, k := range keys {
		n, err := s.rdb.Exists(ctx, k).Result()
		if err != nil {
			t.Fatalf("checking existence of key %q: %v", k, err)
		}
		if n != 0 {
			t.Fatalf("refusing to overwrite pre-existing key %q", k)
		}
	}
}

// TestClusterActorRoundTrip writes an Actor through the store and reads it back
// from a real cluster, then confirms the stored value is binary protobuf (not
// protojson).
func TestClusterActorRoundTrip(t *testing.T) {
	s, ctx := setupClusterTest(t)

	actor := &ateapipb.Actor{
		ActorId:                "cluster-session-1",
		ActorTemplateNamespace: "default",
		ActorTemplateName:      "test-template",
		Status:                 ateapipb.Actor_STATUS_SUSPENDED,
		LatestSnapshotInfo: &ateapipb.SnapshotInfo{
			Type: ateapipb.SnapshotType_SNAPSHOT_TYPE_EXTERNAL,
			Data: &ateapipb.SnapshotInfo_External{
				External: &ateapipb.ExternalSnapshotInfo{
					SnapshotUriPrefix: "gs://b1/f1",
				},
			},
		},
	}

	assertKeysAbsent(ctx, t, s, actorDBKey(actor.ActorId))
	if err := s.CreateActor(ctx, actor); err != nil {
		t.Fatalf("CreateActor failed: %v", err)
	}

	got, err := s.GetActor(ctx, actor.ActorId)
	if err != nil {
		t.Fatalf("GetActor failed: %v", err)
	}

	want := proto.Clone(actor).(*ateapipb.Actor)
	want.Version = 1
	if diff := cmp.Diff(want, got, protocmp.Transform()); diff != "" {
		t.Errorf("actor round-trip mismatch (-want +got):\n%s", diff)
	}

	// The raw value must be binary protobuf: it should decode with proto.Unmarshal
	// and must NOT be valid JSON (protojson always emits a leading '{').
	raw, err := s.rdb.Get(ctx, actorDBKey(actor.ActorId)).Bytes()
	if err != nil {
		t.Fatalf("raw Get failed: %v", err)
	}
	if len(raw) > 0 && raw[0] == '{' {
		t.Errorf("stored actor value looks like JSON, expected binary protobuf: %q", raw)
	}
	decoded := &ateapipb.Actor{}
	if err := proto.Unmarshal(raw, decoded); err != nil {
		t.Errorf("stored actor value is not valid binary protobuf: %v", err)
	}

	// Optimistic-concurrency update must still work against the cluster.
	got.Status = ateapipb.Actor_STATUS_RUNNING
	if err := s.UpdateActor(ctx, got, got.GetVersion()); err != nil {
		t.Fatalf("UpdateActor failed: %v", err)
	}
	if got.Version != 2 {
		t.Errorf("expected version 2 after update, got %d", got.Version)
	}
}

// TestClusterWorkerRoundTrip writes a Worker through the store and reads it back
// from a real cluster, then confirms the stored value is binary protobuf.
func TestClusterWorkerRoundTrip(t *testing.T) {
	s, ctx := setupClusterTest(t)

	worker := &ateapipb.Worker{
		WorkerNamespace: "default",
		WorkerPool:      "pool-1",
		WorkerPod:       "pod-1",
		Ip:              "10.0.0.1",
	}

	assertKeysAbsent(ctx, t, s, workerDBKey(worker.GetWorkerNamespace(), worker.GetWorkerPool(), worker.GetWorkerPod()))
	if err := s.CreateWorker(ctx, worker); err != nil {
		t.Fatalf("CreateWorker failed: %v", err)
	}

	got, err := s.GetWorker(ctx, "default", "pool-1", "pod-1")
	if err != nil {
		t.Fatalf("GetWorker failed: %v", err)
	}

	want := proto.Clone(worker).(*ateapipb.Worker)
	want.Version = 1
	if diff := cmp.Diff(want, got, protocmp.Transform()); diff != "" {
		t.Errorf("worker round-trip mismatch (-want +got):\n%s", diff)
	}

	raw, err := s.rdb.Get(ctx, workerDBKey("default", "pool-1", "pod-1")).Bytes()
	if err != nil {
		t.Fatalf("raw Get failed: %v", err)
	}
	if len(raw) > 0 && raw[0] == '{' {
		t.Errorf("stored worker value looks like JSON, expected binary protobuf: %q", raw)
	}
	decoded := &ateapipb.Worker{}
	if err := proto.Unmarshal(raw, decoded); err != nil {
		t.Errorf("stored worker value is not valid binary protobuf: %v", err)
	}

	got.ActorId = "session-1"
	if err := s.UpdateWorker(ctx, got, got.GetVersion()); err != nil {
		t.Fatalf("UpdateWorker failed: %v", err)
	}

	readBack, err := s.GetWorker(ctx, "default", "pool-1", "pod-1")
	if err != nil {
		t.Fatalf("GetWorker after update failed: %v", err)
	}
	if readBack.GetActorId() != "session-1" || readBack.GetVersion() != 2 {
		t.Errorf("unexpected worker after update: actor_id=%q version=%d", readBack.GetActorId(), readBack.GetVersion())
	}
}

// TestClusterListRoundTrip exercises the list decode paths (fetchActors via
// ListActors, and ListWorkers) against a real cluster under binary encoding,
// comparing the listed records as sets against the exact expected protobufs.
func TestClusterListRoundTrip(t *testing.T) {
	s, ctx := setupClusterTest(t)

	wantActors := map[string]*ateapipb.Actor{}
	wantWorkers := map[string]*ateapipb.Worker{}
	for i := 0; i < 3; i++ {
		actor := &ateapipb.Actor{
			ActorId:                fmt.Sprintf("id%d", i),
			ActorTemplateNamespace: "ns1",
			ActorTemplateName:      "tmpl1",
			Status:                 ateapipb.Actor_STATUS_SUSPENDED,
		}
		assertKeysAbsent(ctx, t, s, actorDBKey(actor.GetActorId()))
		if err := s.CreateActor(ctx, actor); err != nil {
			t.Fatalf("CreateActor(%d) failed: %v", i, err)
		}
		stored := proto.Clone(actor).(*ateapipb.Actor)
		stored.Version = 1
		wantActors[stored.GetActorId()] = stored

		worker := &ateapipb.Worker{
			WorkerNamespace: "ns1",
			WorkerPool:      "pool1",
			WorkerPod:       fmt.Sprintf("pod%d", i),
		}
		assertKeysAbsent(ctx, t, s, workerDBKey(worker.GetWorkerNamespace(), worker.GetWorkerPool(), worker.GetWorkerPod()))
		if err := s.CreateWorker(ctx, worker); err != nil {
			t.Fatalf("CreateWorker(%d) failed: %v", i, err)
		}
		stw := proto.Clone(worker).(*ateapipb.Worker)
		stw.Version = 1
		wantWorkers[stw.GetWorkerPod()] = stw
	}

	actors, _, err := s.ListActors(ctx, 1000, "")
	if err != nil {
		t.Fatalf("ListActors failed: %v", err)
	}
	gotActors := map[string]*ateapipb.Actor{}
	for _, a := range actors {
		if _, dup := gotActors[a.GetActorId()]; dup {
			t.Errorf("duplicate actor in list: %s", a.GetActorId())
		}
		gotActors[a.GetActorId()] = a
	}
	if diff := cmp.Diff(wantActors, gotActors, protocmp.Transform()); diff != "" {
		t.Errorf("ListActors returned unexpected set (-want +got):\n%s", diff)
	}

	workers, err := s.ListWorkers(ctx)
	if err != nil {
		t.Fatalf("ListWorkers failed: %v", err)
	}
	gotWorkers := map[string]*ateapipb.Worker{}
	for _, w := range workers {
		if _, dup := gotWorkers[w.GetWorkerPod()]; dup {
			t.Errorf("duplicate worker in list: %s", w.GetWorkerPod())
		}
		gotWorkers[w.GetWorkerPod()] = w
	}
	if diff := cmp.Diff(wantWorkers, gotWorkers, protocmp.Transform()); diff != "" {
		t.Errorf("ListWorkers returned unexpected set (-want +got):\n%s", diff)
	}
}

// TestClusterCorruptValues verifies that malformed, empty, and identity-
// mismatched stored values are rejected rather than silently surfacing as
// wrong records, across both the Get* and List* decode paths. Empty bytes are
// a valid encoding of a zero message under proto.Unmarshal, so the key identity
// checks must catch them; a valid protobuf stored under the wrong key must be
// caught the same way.
func TestClusterCorruptValues(t *testing.T) {
	t.Run("garbage actor value fails GetActor", func(t *testing.T) {
		s, ctx := setupClusterTest(t)
		assertKeysAbsent(ctx, t, s, actorDBKey("garbage"))
		if err := s.rdb.Set(ctx, actorDBKey("garbage"), "not-a-protobuf-\xff\xfe", 0).Err(); err != nil {
			t.Fatalf("seeding garbage actor failed: %v", err)
		}
		if _, err := s.GetActor(ctx, "garbage"); err == nil {
			t.Errorf("expected GetActor to fail on a non-protobuf value, got nil")
		}
	})

	t.Run("empty worker value rejected by Get and List", func(t *testing.T) {
		s, ctx := setupClusterTest(t)
		assertKeysAbsent(ctx, t, s, workerDBKey("ns1", "pool1", "ghost"))
		if err := s.rdb.Set(ctx, workerDBKey("ns1", "pool1", "ghost"), "", 0).Err(); err != nil {
			t.Fatalf("seeding empty worker failed: %v", err)
		}
		if _, err := s.GetWorker(ctx, "ns1", "pool1", "ghost"); err == nil {
			t.Errorf("expected GetWorker to reject an empty value, got nil")
		}
		if _, err := s.ListWorkers(ctx); err == nil {
			t.Errorf("expected ListWorkers to reject the empty-value worker key, got nil")
		}
	})

	t.Run("empty actor value rejected by Get and List", func(t *testing.T) {
		s, ctx := setupClusterTest(t)
		assertKeysAbsent(ctx, t, s, actorDBKey("ghost"))
		if err := s.rdb.Set(ctx, actorDBKey("ghost"), "", 0).Err(); err != nil {
			t.Fatalf("seeding empty actor failed: %v", err)
		}
		if _, err := s.GetActor(ctx, "ghost"); err == nil {
			t.Errorf("expected GetActor to reject an empty value, got nil")
		}
		if _, _, err := s.ListActors(ctx, 1000, ""); err == nil {
			t.Errorf("expected ListActors to reject the empty-value actor key, got nil")
		}
	})

	t.Run("identity-mismatched worker rejected by Get and List", func(t *testing.T) {
		s, ctx := setupClusterTest(t)
		// A valid Worker proto, but stored under a key with a different pod.
		bytes, err := proto.Marshal(&ateapipb.Worker{
			WorkerNamespace: "ns1", WorkerPool: "pool1", WorkerPod: "real", Version: 1,
		})
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}
		assertKeysAbsent(ctx, t, s, workerDBKey("ns1", "pool1", "wrong"))
		if err := s.rdb.Set(ctx, workerDBKey("ns1", "pool1", "wrong"), bytes, 0).Err(); err != nil {
			t.Fatalf("seeding mismatched worker failed: %v", err)
		}
		if _, err := s.GetWorker(ctx, "ns1", "pool1", "wrong"); err == nil {
			t.Errorf("expected GetWorker to reject an identity-mismatched value, got nil")
		}
		if _, err := s.ListWorkers(ctx); err == nil {
			t.Errorf("expected ListWorkers to reject the identity-mismatched worker, got nil")
		}
	})

	t.Run("identity-mismatched actor rejected by Get and List", func(t *testing.T) {
		s, ctx := setupClusterTest(t)
		// A valid Actor proto, but stored under a key with a different id.
		bytes, err := proto.Marshal(&ateapipb.Actor{
			ActorId: "real", ActorTemplateNamespace: "ns1", ActorTemplateName: "tmpl1", Version: 1,
		})
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}
		assertKeysAbsent(ctx, t, s, actorDBKey("wrong"))
		if err := s.rdb.Set(ctx, actorDBKey("wrong"), bytes, 0).Err(); err != nil {
			t.Fatalf("seeding mismatched actor failed: %v", err)
		}
		if _, err := s.GetActor(ctx, "wrong"); err == nil {
			t.Errorf("expected GetActor to reject an identity-mismatched value, got nil")
		}
		if _, _, err := s.ListActors(ctx, 1000, ""); err == nil {
			t.Errorf("expected ListActors to reject the identity-mismatched actor, got nil")
		}
	})
}

// TestClusterLockBinarySafe verifies lock acquire/release is unchanged by the
// encoding switch, including a token carrying an embedded NUL byte. The
// release Lua compares the opaque token by byte equality and never touches the
// record encoding.
func TestClusterLockBinarySafe(t *testing.T) {
	s, ctx := setupClusterTest(t)

	key := "test-lock"
	token := "tok\x00with-nul"
	other := "tok\x00other"
	ttl := 10 * time.Second

	assertKeysAbsent(ctx, t, s, key)
	acquired, err := s.AcquireLock(ctx, key, token, ttl)
	if err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}
	if !acquired {
		t.Fatalf("expected lock to be acquired")
	}

	// Wrong token must not release.
	if err := s.ReleaseLock(ctx, key, other); err != nil {
		t.Fatalf("ReleaseLock(wrong) failed: %v", err)
	}
	acquired, err = s.AcquireLock(ctx, key, other, ttl)
	if err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}
	if acquired {
		t.Errorf("lock was released by the wrong token")
	}

	// Correct token releases.
	if err := s.ReleaseLock(ctx, key, token); err != nil {
		t.Fatalf("ReleaseLock(correct) failed: %v", err)
	}
	acquired, err = s.AcquireLock(ctx, key, other, ttl)
	if err != nil {
		t.Fatalf("AcquireLock failed: %v", err)
	}
	if !acquired {
		t.Errorf("expected lock to be free after correct release")
	}
}
