# shared-base: benchmark code for the LLIFS density + latency results

This directory holds everything needed to reproduce the shared-copy-on-write-base
(LLIFS GVISOR-3) results reported in `docs/blog-shared-base-memory.md` and
`../density-prototype-findings.md`: the gVisor change, the reproduction ladder, the
orchestration scripts, and the agent workload.

- **`gvisor-shared-base.patch`** -- the canonical combined gVisor patch (applies to upstream
  gVisor `928199eb9`). Base memory shared `MAP_PRIVATE` copy-on-write across restored sandboxes,
  delta-only `SaveTo`, base-overlay `LoadFrom`, and the `runsc checkpoint --shared-base` / restore
  plumbing. This is the buildable artifact; the three incremental patches in the parent directory
  are development history and must not be sequence-applied (they overlap on `sandbox.go`).
- **`make-gvisor-fork.sh`** -- clones gVisor at the pinned commit, applies the combined patch, and
  creates a `shared-base-density` branch ready to build and (later) push to the gVisor fork.
- **`REPRODUCE.md`** -- the full ladder: host, toolchain, build, run, expected numbers.
- **`runs/`** -- the exact orchestration scripts used on the KVM host (one per experiment; see the
  table in REPRODUCE.md for which script produced which finding).
- **`agent/`** -- the real workload: a google-adk 2.3.0 Python agent with the LLM endpoint mocked
  (`agent.py` instrumented for the correctness/latency runs, `agent-basic.py` minimal),
  plus the `Dockerfile` used to build its rootfs.

Related, in the parent `benchmarking/` directory: the standalone `density_smoke.c` (OS COW
primitive), `cr_workload.c` (C checkpoint/restore workload), `verify_share/` and `lazy_verify/`
(Terrapin verify-before-expose), and the in-tree pgalloc tests.

Nothing here is published yet. The gVisor branch and this repository are pushed to the fork
remote on request.
