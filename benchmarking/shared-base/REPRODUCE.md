# Reproducing the shared-base density and latency results

This is the exact ladder used to produce the numbers in
`../density-prototype-findings.md` (§§9-13f) and `docs/blog-shared-base-memory.md`.

The **density (flatten)** results require the gVisor **KVM** platform (`/dev/kvm`); the
**restore-vs-cold-start latency** result is platform-independent and also runs on **systrap**
(no `/dev/kvm`). See the F9 / GVISOR-6 discussion in the findings for why.

## 1. Host

A machine with a working `/dev/kvm`. What was used: a **GCE `n2-standard-4`** (4 vCPU, ~15 GiB),
**Ubuntu 24.04**, with **nested virtualization enabled** (GCE's nested virt is Linux-KVM-backed,
which gVisor's KVM platform needs; Apple-silicon and VMware nesting crashed under gVisor-KVM).
Any bare-metal Linux host with `/dev/kvm` also works. Confirm: `ls -l /dev/kvm` and
`sudo kvm-ok`.

## 2. Toolchain (Ubuntu 24.04)

```
sudo apt-get install -y git g++ clang llvm libbpf-dev gcc-multilib libc6-dev-i386 make curl \
                        gcc-aarch64-linux-gnu g++-aarch64-linux-gnu \
                        python3-venv python3-pip docker.io
# bazelisk (pins bazel 8.3.1 from gVisor's .bazelversion)
curl -fsSL -o ~/bin/bazel https://github.com/bazelbuild/bazelisk/releases/download/v1.20.0/bazelisk-linux-amd64
chmod +x ~/bin/bazel   # ensure ~/bin on PATH
```
(The aarch64 cross tools must be installed AFTER `gcc-multilib`; multilib removes them, so
reinstall them last. The x86_64 build needs `gnu/stubs-32.h` from `gcc-multilib` for XDP and the
aarch64 cross-gcc for the VDSO genrule.)

## 3. Build patched gVisor

```
# Apply the separately released four-patch shared-base series to gVisor 928199eb9.
bazel build --jobs=4 --local_resources=memory=12000 //runsc:runsc
bazel test //pkg/sentry/pgalloc:pgalloc_test   # the in-tree base/delta tests
```
The runsc binary is `bazel-bin/runsc/runsc_/runsc`.

## 4. Run the experiments

The scripts in `runs/` are the exact ones used. They assume `~/gvisor` is the built fork
(`R=~/gvisor/bazel-bin/runsc/runsc_/runsc`) and are launched from `$HOME`; edit `H=` at the top
if your layout differs. Each writes its result to a `*.txt` file. Mapping to the findings:

| script | measures | finding | headline |
| --- | --- | --- | --- |
| `runs/measure.sh` | N-clone flatten over a shared base (touch-all, 256 MiB) | §13b | 5.7x at N=8 (runsc, KVM) |
| `runs/ceiling.sh` | density ceiling, N escalated 32->600 | §13c | ~300 sandboxes / 15 GiB |
| `runs/gomem.sh` | per-sandbox memory floor (sentry/gofer, Go anon) | §13d | ~20 MiB, ~half Go runtime |
| `runs/gotune.sh` | GOGC/GOMEMLIMIT effect on the floor | §13d | no-op (structural) |
| `runs/lat2.sh` | cold-start vs restore latency (wall + CPU) | §13d | 11.4s -> 0.6s; ~4-8x cheaper CPU |
| `runs/expA.sh` | warm-vs-thrash + correctness + clone independence | §13e | warm; state bit-exact |
| `runs/expB.sh` | burst restore, N=50/100 concurrent | §13e | CPU-throughput-bound |
| `runs/expC.sh` | snapshot storage + systrap latency | §13f | ~248 KiB/agent; systrap holds |
| `runs/adkexp.sh` + `runs/buildimg.sh` + `agent/` | real google-adk agent flatten | §13d | 2.9x (floor-limited) |
| `runs/adkexp2.sh` | allocated-vs-resident working set | §13d | cold padding does not raise flatten |

`lat2.sh` is the corrected latency runner. `runs/buildimg.sh` builds the ADK Docker rootfs from
`agent/Dockerfile` + `agent/agent.py` (the instrumented mock-LLM agent; `agent-basic.py` is the
minimal variant). The workloads themselves: `../cr_workload.c` (C checkpoint/restore workload)
and `agent/` (Python google-adk 2.3.0, Python 3.12, LLM endpoint mocked, no network).

## 5. Expected numbers and environment

Full results and the honest caveats are in `../density-prototype-findings.md` (§§13a-13f).
Environment of record: GCE `n2-standard-4`, Ubuntu 24.04, nested virtualization; gVisor
`928199eb9`; bazel 8.3.1; google-adk 2.3.0 on Python 3.12.

## Related code (parent directory)

- `../gvisor3-s1.pgalloc.patch`, `../c1-restore-plumbing.patch`, and
  `../c1b-checkpoint-plumbing.patch` are experimental development records. They overlap and are not
  the release series; use the separately published four-patch gVisor series for reproduction.
- `../sharedbase_test.go`, `../saverestore_test.go`, `../flatten_test.go` -- the in-tree pgalloc
  tests (also contained in the combined patch).
- `../density_smoke.c` -- the standalone Linux `MAP_PRIVATE` copy-on-write smoke test (§ "does the
  OS even do this").
- `../verify_share/`, `../lazy_verify/` -- Terrapin verify-before-expose, eager and lazy (uffd+CAS).
- `../rhz.go`, `../memsnap.go`, `../pagediff.go` -- restore-hazard and memory-delta measurement.
