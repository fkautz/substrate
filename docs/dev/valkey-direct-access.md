# Accessing valkey directly

Valkey is the state store used by `ate-api-server` to track actor and worker records. Direct access is useful for debugging state issues.

> **Warning:** Avoid destructive commands (`FLUSHALL`, `DEL`, etc.) on a live cluster.

To open a `valkey-cli` session:

1. `kubectl exec -n=ate-system -it valkey-cluster-0 -- valkey-cli -h valkey-cluster-service -c --tls --cacert /etc/valkey-ca/ca.crt --cert /run/servicedns.podcert.ate.dev/credential-bundle.pem --key /run/servicedns.podcert.ate.dev/credential-bundle.pem`

## Record encoding (binary protobuf)

`Actor` and `Worker` records are stored as **binary protobuf**, not JSON, so a
raw `GET actor:<id>` returns non-human-readable bytes. To decode a value, pipe
the raw bytes through `protoc`:

```sh
# Actor (key: actor:<id>)
valkey-cli --no-raw ... GET actor:my-actor \
  | protoc --decode=ateapi.Actor -I pkg/proto/ateapipb pkg/proto/ateapipb/ateapi.proto

# Worker (key: worker:<namespace>:<pool>:<pod>)
valkey-cli --no-raw ... GET worker:default:pool-1:pod-1 \
  | protoc --decode=ateapi.Worker -I pkg/proto/ateapipb pkg/proto/ateapipb/ateapi.proto
```

> The worker change-notification pub/sub channel (`worker-changes`) still carries
> protojson-wrapped events and remains human-readable; only the stored records
> are binary.

## Deploying the binary-protobuf cutover

The switch from protojson to binary protobuf is a **hard cutover with no
migration path**: protojson and binary protobuf cannot decode each other.

- **Flush before serving.** Any pre-existing protojson records must be flushed;
  the new binary will fail to decode them. Records are a re-derivable cache, so
  flushing is safe.
- **No rolling deploy across the cutover.** While old (protojson) and new
  (binary) `ate-api-server` replicas share the keyspace, each fails to decode the
  other's records. Deploy as: stop all `ate-api-server` replicas → flush the
  keyspace → start the new version. A zero-downtime rolling update is not safe
  for this change.
