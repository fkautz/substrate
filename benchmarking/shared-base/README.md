# shared-base: benchmark code for the density and latency results

This directory holds everything needed to reproduce the shared-copy-on-write-base
results reported in `../density-prototype-findings.md`: the gVisor change, the reproduction ladder, the
orchestration scripts, and the agent workload.

- **`REPRODUCE.md`** -- the full ladder: host, toolchain, build, run, expected numbers.
- **`prepare-gvisor.sh`** -- clones the pinned upstream gVisor revision, verifies the patch
  checksums, and applies the four-patch series in order.
- **`patches/gvisor-shared-base-v1/`** -- a four-part `git format-patch` series against gVisor
  `928199eb9`, with a cover letter and SHA-256 checksums.
- **`runs/`** -- one self-contained runner per measurement. Every runner begins with `Test:`,
  `Why:`, and `Output:` comments; see the table in REPRODUCE.md.
- **`agent/`** -- the real workload: a google-adk 2.3.0 Python agent with the LLM endpoint mocked
  (`agent.py` instrumented for the correctness/latency runs, `agent-basic.py` minimal),
  plus the `Dockerfile` used to build its rootfs.

Related, in the parent `benchmarking/` directory: the standalone `density_smoke.c` (OS COW
primitive), `cr_workload.c` (C checkpoint/restore workload), `verify_share/` and `lazy_verify/`
(Terrapin verify-before-expose), and the in-tree pgalloc tests.

The release series separates shared-base mapping, base/delta save and restore, restore plumbing,
and checkpoint plumbing. The older incremental patches in the parent directory are retained only
as experimental history; they overlap and must not be applied as a series.
