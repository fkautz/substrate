# Record Encoding Benchmarks

How much memory does one control-plane Actor record cost, and how much can a
different encoding save? This measures five encodings end to end (size on the wire,
size in Valkey, and encode/decode CPU), all round-trip verified. The runnable
benchmark lives in [`internal/encodingbench`](../internal/encodingbench); anyone
can reproduce or extend these numbers.

> Provenance: measured with the `internal/encodingbench` package against the
> `ateapipb.Actor` schema at commit `d0c7f572144e46ff3974d57dbebd9720c539fa60`
> (`gitoid:commit:sha1:d0c7f572144e46ff3974d57dbebd9720c539fa60`), 2026-06-25.
> Co-created using Claude Opus 4.8.

Context: the store keeps Actor and Worker records in Valkey and currently encodes
them as **protojson** (protobuf serialized to JSON text). See
[`state-store.md`](./state-store.md) for why record size matters: the system
targets 1 billion actors, all resident in RAM.

## TL;DR

- **Switch protojson to binary protobuf.** It is **42% smaller per record** and
  also **4x faster** to encode. Pure win, no new dependencies, keeps schema
  evolution.
- **Then apply field-level trims** (UUIDs as bytes, snapshot URI stored relative),
  implemented as a real protobuf schema (`trimmedpb.TrimmedActor`). Together with
  binary protobuf that is **61% smaller per record (731 to 283 B on jemalloc)**, at
  the same speed as plain protobuf. No compression, no dictionary.
- **Dictionary-compressed protobuf is smallest (72%)** but its encode CPU with a
  raw dictionary is ~1 ms/record, likely too slow for the write path without a
  properly trained/compiled dictionary. Reach for it only if the first two are not
  enough.
- Avro, MessagePack, CBOR, Cap'n Proto, and FlatBuffers are not worth it here
  (marginal, larger, or they optimize read latency at the cost of size). See
  [state-store.md](./state-store.md) "other formats" discussion.

## How to run

```sh
# Size (value bytes) for every encoding, plus the comparison table:
go test ./internal/encodingbench/ -run TestSummary -v

# One encoding at a time (each is its own test):
go test ./internal/encodingbench/ -run TestFieldTrims -v

# Encode/decode CPU:
go test ./internal/encodingbench/ -run x -bench . -benchmem

# Real per-record memory in Valkey (value + key + engine overhead).
# Production-accurate run against the jemalloc valkey/valkey:8.0 image (the same
# build the cluster runs). Use a THROWAWAY instance; the test FLUSHALLs between
# encodings:
docker run -d --name vkbench -p 6400:6379 valkey/valkey:8.0 \
  valkey-server --save '' --appendonly no
ENCODINGBENCH_VALKEY_ADDR=localhost:6400 \
  go test ./internal/encodingbench/ -run TestValkeyPerRecord -v
docker rm -f vkbench
```

The field-trims schema (`internal/encodingbench/trimmedpb/trimmed.proto`) is
checked in already generated; regenerate it with `go generate ./internal/encodingbench/...`
if you change it (uses the repo's pinned protoc via `hack/protoc.sh`).

Every codec is round-trip verified inside the tests (`Decode(Encode(a))` must
`proto.Equal` the original), so the reported sizes are for genuinely lossless,
decodable encodings.

## Results

Measured over a deterministic 50,000-record corpus of representative Actors
(fixed RNG seed, so runs are reproducible).

### Value size (encoded bytes per record, excluding Valkey overhead)

| Encoding | B/value | vs protojson |
|---|---|---|
| protojson (current) | 609 | baseline |
| binary protobuf | 301 | 50% smaller |
| binary + field trims | 170 | 72% smaller |
| zstd(binary), no dict | 219 | 64% smaller |
| zstd(binary), raw dict | 112 | 82% smaller |
| shared-context stream* | 66 | 89% smaller |

\* All records compressed as one stream, divided by count. Not a per-record
encoding, just the floor a perfectly trained dictionary approaches. The large gap
between this and per-record no-dict (219 B) is what tells us the records are highly
self-similar, hence the dictionary opportunity.

### In-Valkey memory (value + 42 B key + engine overhead), measured

Measured against the production jemalloc `valkey/valkey:8.0` image. The libc column
is a Homebrew Valkey for reference; jemalloc runs ~12-13 B/record higher because it
rounds allocations to size classes. Ratios are the same either way.

| Encoding | jemalloc B/record | vs protojson | libc B/record |
|---|---|---|---|
| protojson (current) | 731 | baseline | 718 |
| binary protobuf | 426 | 42% smaller | 413 |
| binary + field trims | 283 | 61% smaller | 269 |
| zstd(binary), no dict | 323 | 56% smaller | 310 |
| zstd(binary), raw dict | 216 | 70% smaller | 203 |

The per-record reductions are smaller than the value-only ones because the fixed
~120 B/key overhead (the 42 B key plus Valkey's dict entry, object header, and
cluster bookkeeping) is the same for every encoding. Compression cannot touch it.

### Encode / decode CPU (ns per record)

| Encoding | encode | decode |
|---|---|---|
| protojson (current) | 3124 | 4661 |
| binary protobuf | 721 | 925 |
| binary + field trims | 719 | 953 |
| zstd(binary), no dict | 19082 | 3639 |
| zstd(binary), raw dict | 1020971 | 3076 |

## Per-encoding notes

- **binary protobuf** wins on every axis versus protojson: half the size, ~4x
  faster encode, ~5x faster decode, same schema and evolution story. The only cost
  is human-readability in `valkey-cli`, mitigable with a decode helper in
  `kubectl-ate`.
- **binary + field trims** is the best size-for-CPU: 61% off at essentially the same
  speed as plain protobuf, because two 36-char UUID strings become 16-byte fields and
  the snapshot URI is stored as just its template-relative suffix (reconstructed from
  fields already in the record). This is a **real protobuf schema**
  (`trimmedpb.TrimmedActor`, generated from `trimmed.proto`), so the number includes
  protobuf field tags; in production it would be the corresponding change to the
  `ateapipb.Actor` schema (a `bytes` field for IDs, a suffix-only snapshot field).
- **zstd(binary), no dict** is middling: 56% off but ~19 us/encode (still tiny in
  absolute terms against a 100 ms budget, but ~26x the cost of plain protobuf, and
  it is larger *and* slower than field trims, so field trims dominate it).
- **zstd(binary), raw dict** is the smallest viable per-record encoding (72% off),
  but the raw-content dictionary makes encode ~1 ms/record at best-compression,
  which is likely too slow for the write path. A properly trained and compiled
  dictionary would encode far faster; klauspost's `BuildDict` trainer currently
  returns empty, so a real deployment would train with the C `zstd --train` tool.
  Treat this row's size as the target and its CPU as a warning to invest in a
  trained dictionary before adopting it.

## Capacity impact at the 1B-actor north star

Provisioned RAM scales ~5x the raw dataset (2x replica, ~2.5x headroom):

| Encoding | per record (jemalloc) | ~raw at 1B | ~provisioned (5x) |
|---|---|---|---|
| protojson (current) | 731 B | 731 GB | ~3.7 TB |
| binary protobuf | 426 B | 426 GB | ~2.1 TB |
| binary + field trims | 283 B | 283 GB | ~1.4 TB |
| zstd(binary), raw dict | 216 B | 216 GB | ~1.1 TB |

Binary protobuf plus field trims removes ~2.2 TB of provisioned RAM at the 1B
target relative to protojson, with no compression CPU and no dictionary to manage.

## Recommendation

1. **Binary protobuf** now: smaller and faster, localized to the marshal/unmarshal
   calls in `ateredis.go`, plus a 1-byte format tag and try-binary-else-JSON read
   path for migration (workers are re-derivable via the syncer; actors re-encode
   on next write).
2. **Field trims** as schema changes (bytes IDs, suffix-only snapshot URI): another
   large cut at the best CPU profile, still pure protobuf with schema evolution.
3. **Dictionary compression** only if 1 and 2 do not meet the runway target, and
   only with a trained/compiled dictionary plus an explicit encode-CPU budget and a
   dictionary-lifecycle plan (versioned `dict ID` embedded in each value so old data
   stays readable; retrain as the data drifts).

## Caveats

- In-Valkey numbers are from the production jemalloc `valkey/valkey:8.0` image
  (run in Docker); the libc column is a Homebrew build for reference. jemalloc is
  ~12-13 B/record higher due to size-class rounding; ratios are identical.
- Value sizes are exact; in-Valkey sizes include the fixed per-key overhead, which
  is identical across encodings (same keys), so cross-encoding deltas are clean.
- The field-trims schema (`trimmedpb.TrimmedActor`) mirrors `ateapipb.Actor`'s field
  numbers so tag widths match a real migration. Its snapshot handling assumes the URI
  follows the template-relative layout, falling back to a full-URI field otherwise.
- The raw dictionary (~19 KB) is shipped once, not per record. Its encode CPU is a
  blocker until replaced by a trained dictionary.
