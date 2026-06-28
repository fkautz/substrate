# Workload-identity expansion: growing `atelet.WorkloadSpec`

Status: Proposal / advocacy
Audience: atelet / ateom / control-plane maintainers
Companion to: `docs/llifs.spec` (SR-5, §15.8 LLWI1, §11.4 compatibility matrix)

## Summary

LLIFS binds every persisted snapshot (a State Root) to a **workload identity**
(`SR-5`, encoded as `LLWI1`, §15.8). On restore the supplied workload must match
the committed identity exactly, or the restore is rejected. The identity exists for
one reason: **restore safety**. Reapplying a persisted memory/filesystem/runtime
delta against a workload configured differently from the one that produced it can
be incorrect or unsafe, and a difference that is not part of the identity goes
undetected.

`LLWI1` is specified as the **target** identity: it commits every restore-visible
per-workload field. The current `atelet.WorkloadSpec` carries far fewer fields, so
today the encoder cannot populate most of the identity. **This document specifies
the proto changes needed to close that gap.** Per the spec, `LLWI1` is deliberately
**not** pared down to the current proto; the proto grows up to `LLWI1`.

This is a release blocker for v1.0 (`ENC-WI-1`): until the proto supplies a field,
`LLWI1` cannot commit a real value for it, and committing a field the proto cannot
supply (always its default) is exactly the restore-safety bug the identity prevents.

## Current proto (for reference)

`internal/proto/ateletpb/atelet.proto`:

```proto
message WorkloadSpec {
  repeated Container containers  = 1;
  string             pause_image = 2;
}
message Container {
  string            name    = 1;
  string            image   = 2;
  repeated string   command = 3;   // single list; no separate args
  repeated EnvEntry env     = 4;
}
message EnvEntry { string name = 1; string value = 2; }
```

Comment in the proto: *"WorkloadSpec parallels Pod, but with far fewer configurable
fields."* `ateom`'s reduced `Container` is smaller still (`ENC-WI-1` notes it is
insufficient).

## What `LLWI1` commits today vs what the proto supplies

| `LLWI1` field (target) | scope | in proto now? |
|---|---|---|
| `pause_image` | pod | yes |
| `name`, `image` | container | yes |
| `command` | container | yes (as `command`) |
| `args` | container | no (folded into `command`) |
| `env` | container | yes |
| `working_dir` | container | **no** |
| run-as `user`/`group` | container | **no** |
| capabilities add/drop | container | **no** |
| security context (privileged, ro-rootfs, no-new-privs, seccomp/AppArmor/SELinux) | container | **no** |
| mount / volume-device topology (path, sub-path, read-only, source) | container | **no** |
| devices | container | **no** |
| resource limits (memory, cpu) | container | **no** |
| `hostname` | pod | **no** |
| host-namespace flags (network/pid/ipc), share-process-namespace | pod | **no** |
| pod security context | pod | **no** |

## Proposed proto additions

Add new field numbers (wire-compatible); populate them in the control plane before
relying on the identity. Suggested shape:

```proto
message WorkloadSpec {
  repeated Container containers  = 1;
  string             pause_image = 2;
  string             hostname           = 3;
  bool               host_network       = 4;
  bool               host_pid           = 5;
  bool               host_ipc           = 6;
  bool               share_process_ns   = 7;
  SecurityContext    pod_security       = 8;
}

message Container {
  string            name        = 1;
  string            image       = 2;
  repeated string   command     = 3;   // entrypoint
  repeated EnvEntry env         = 4;
  repeated string   args        = 5;   // split out from command
  string            working_dir = 6;
  SecurityContext   security    = 7;
  Capabilities      capabilities = 8;
  repeated Mount    mounts      = 9;
  repeated Device   devices     = 10;
  ResourceLimits    resources   = 11;
}

message SecurityContext {
  int64  run_as_user  = 1;   // -1 = unset
  int64  run_as_group = 2;   // -1 = unset
  bool   privileged              = 3;
  bool   read_only_root_filesystem = 4;
  bool   allow_privilege_escalation = 5;
  bool   run_as_non_root         = 6;
  string seccomp                 = 7;   // profile type+ref, "" = unset
  string apparmor                = 8;
  string selinux                 = 9;
}
message Capabilities { repeated string add = 1; repeated string drop = 2; }
message Mount {
  string mount_path = 1; string sub_path = 2; bool read_only = 3;
  string source_name = 4; string source_type = 5;
}
message Device { string path = 1; }
message ResourceLimits { uint64 memory_limit_bytes = 1; uint64 cpu_limit_milli = 2; }
```

The `LLWI1` encoding in §15.8 already mirrors these fields and their canonical
ordering/sorting rules; growing the proto to this shape lets the encoder populate
`LLWI1` directly.

`ateom`'s reduced `Container` must carry the same fields (or carry the full
`atelet.WorkloadSpec` through unchanged), per `ENC-WI-1` / §12.

## Avoid double-authority with the compatibility matrix

Some of these attributes overlap the base descriptor's **compatibility matrix**
(`§11.4 MATRIX-1`), which already commits `seccomp_tag`, `cgroup_tag`,
`namespace_tag`, and `abi_tag` as per-base/runtime invariants. The rule (`ENC-WI-2`):

- An attribute that is fixed by the base/runtime lives in the **matrix** and is
  enforced by `MATCH-COMPAT` (`MATRIX-3`).
- An attribute that is configurable **per workload** lives in `WorkloadSpec` /
  `LLWI1`.
- The same authority MUST NOT be committed in both places.

For seccomp / namespaces / cgroups specifically: decide per field whether the
system exposes a per-workload override. If it does, the per-workload value goes in
`WorkloadSpec`+`LLWI1` (the effective value); if it does not, leave it in the
matrix and omit it from `WorkloadSpec`.

## Migration and compatibility

- New proto fields are additive (new field numbers) and wire-compatible, but the
  **identity is only correct once the control plane actually populates them**.
  Partial population yields a wrong (too-coarse) identity, so roll out
  populate-before-rely.
- `LLWI1` is already the v2 target encoding; no version bump is needed as the proto
  catches up (the encoding is fixed; the proto simply supplies real values instead
  of defaults). Any field added **beyond** this set later requires a joint
  proto+`LLWI1` change and an `LLWI1` version bump (`ENC-WI-2`).
- Add conformance vectors for a populated multi-field `WorkloadSpec` (`§15.10`).

## Suggested sequencing (highest restore-safety value first)

1. `args` split, `working_dir`, run-as `user`/`group` (process identity and entry).
2. Container `security` context + `capabilities` (privilege surface).
3. `mounts` / volume topology and `devices` (filesystem and device surface the
   upper delta assumes).
4. `resource_limits` (memory limit bounds the guest address space) and pod-scope
   `hostname` / namespace flags.

## Open questions for the team

- Which of seccomp / namespaces / cgroups are per-workload vs per-base here?
- Does the platform expose volume sources the identity should distinguish by type,
  or only by mount path + read-only?
- Should `ateom`'s `Container` carry the full set or pass the `atelet.WorkloadSpec`
  through verbatim?
