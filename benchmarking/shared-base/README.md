# shared-base: benchmark code for the LLIFS density + latency results

This directory holds everything needed to reproduce the shared-copy-on-write-base
(LLIFS GVISOR-3) results reported in `docs/blog-shared-base-memory.md` and
`../density-prototype-findings.md`: the gVisor change, the reproduction ladder, the
orchestration scripts, and the agent workload.

- **`REPRODUCE.md`** -- the full ladder: host, toolchain, build, run, expected numbers.
- **`runs/`** -- the exact orchestration scripts used on the KVM host (one per experiment; see the
  table in REPRODUCE.md for which script produced which finding).
- **`agent/`** -- the real workload: a google-adk 2.3.0 Python agent with the LLM endpoint mocked
  (`agent.py` instrumented for the correctness/latency runs, `agent-basic.py` minimal),
  plus the `Dockerfile` used to build its rootfs.

Related, in the parent `benchmarking/` directory: the standalone `density_smoke.c` (OS COW
primitive), `cr_workload.c` (C checkpoint/restore workload), `verify_share/` and `lazy_verify/`
(Terrapin verify-before-expose), and the in-tree pgalloc tests.

The gVisor implementation is intentionally not bundled here. It will be published separately as
a reviewable four-patch series covering the shared-base mapping, base/delta save and restore,
restore plumbing, and checkpoint plumbing.
