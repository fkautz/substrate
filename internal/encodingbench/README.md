# encodingbench

A standalone, shareable benchmark for how the control-plane Actor record could be
encoded, comparing size (on the wire and in Valkey) and encode/decode CPU across
five encodings. Every codec is round-trip verified, so reported sizes are for
lossless, decodable encodings.

Writeup and guidance: [`docs/record-encoding-benchmarks.md`](../../docs/record-encoding-benchmarks.md).

## Run it

```sh
# Comparison table of value sizes:
go test ./internal/encodingbench/ -run TestSummary -v

# One encoding at a time (each is its own test):
go test ./internal/encodingbench/ -run TestBinaryProto -v
go test ./internal/encodingbench/ -run TestFieldTrims -v
go test ./internal/encodingbench/ -run TestZstdRawDict -v

# Encode/decode CPU:
go test ./internal/encodingbench/ -run x -bench . -benchmem

# Real per-record memory in Valkey (value + key + engine overhead).
# Use a THROWAWAY instance; the test FLUSHALLs between encodings:
valkey-server --port 6399 --save '' --appendonly no --daemonize yes
ENCODINGBENCH_VALKEY_ADDR=localhost:6399 \
  go test ./internal/encodingbench/ -run TestValkeyPerRecord -v
```

## Encodings under test

| Test | Encoding |
|---|---|
| `TestProtojson` | protojson (current production encoding) |
| `TestBinaryProto` | binary protobuf |
| `TestFieldTrims` | binary protobuf with field-level trims (IDs as bytes, snapshot URI relative) |
| `TestZstdNoDict` | zstd over binary protobuf, no dictionary |
| `TestZstdRawDict` | zstd over binary protobuf, raw-content shared dictionary |

The corpus is deterministic (fixed RNG seed), so results are reproducible. Edit
`CorpusSize` or the record shape in `encodingbench.go` to model your own data.
