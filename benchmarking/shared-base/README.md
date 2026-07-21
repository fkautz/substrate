# shared-base: benchmark code for the LLIFS density + latency results

This directory holds everything needed to reproduce the shared-copy-on-write-base
(LLIFS GVISOR-3) results reported in `docs/blog-shared-base-memory.md` and
`../density-prototype-findings.md`: the gVisor change, the reproduction ladder, the
orchestration scripts, and the agent workload.

- **`REPRODUCE.md`** -- the full ladder: host, toolchain, build, run, expected numbers.
- **`patches/gvisor-shared-base-v1/`** -- a four-part `git format-patch` series against gVisor
  `928199eb9`, with a cover letter and SHA-256 checksums.
- **`runs/`** -- the exact orchestration scripts used on the KVM host (one per experiment; see the
  table in REPRODUCE.md for which script produced which finding).
- **`agent/`** -- the real workload: a google-adk 2.3.0 Python agent with the LLM endpoint mocked
  (`agent.py` instrumented for the correctness/latency runs, `agent-basic.py` minimal),
  plus the `Dockerfile` used to build its rootfs.

Related, in the parent `benchmarking/` directory: the standalone `density_smoke.c` (OS COW
primitive), `cr_workload.c` (C checkpoint/restore workload), `verify_share/` and `lazy_verify/`
(Terrapin verify-before-expose), and the in-tree pgalloc tests.

The release series separates shared-base mapping, base/delta save and restore, restore plumbing,
and checkpoint plumbing. The older incremental patches in the parent directory are retained only
as experimental history; they overlap and must not be applied as a series.
