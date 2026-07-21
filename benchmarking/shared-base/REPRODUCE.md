# Reproducing the shared-base density and latency results

This is the exact ladder used to produce the numbers in
`../density-prototype-findings.md` (§§9-13f).

The **density (flatten)** results require the gVisor **KVM** platform (`/dev/kvm`); the
**restore-vs-cold-start latency** result is platform-independent and also runs on **systrap**
(no `/dev/kvm`). The findings document why host KVM support is required only for the KVM runs.

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

## 3. Prepare and build patched gVisor

```
cd <substrate-checkout>/benchmarking/shared-base
./prepare-gvisor.sh "$HOME/gvisor"
cd "$HOME/gvisor"
bazel build --jobs=4 --local_resources=memory=12000 //runsc:runsc
bazel test //pkg/sentry/pgalloc:pgalloc_test   # the in-tree base/delta tests
```
The preparation script refuses to overwrite an existing destination, pins upstream revision
`928199eb9`, verifies `SHA256SUMS`, and applies patches 1 through 4 with `git am`. The runsc binary
is `bazel-bin/runsc/runsc_/runsc`.

## 4. Run the experiments

The scripts in `runs/` preserve the exact test operations used for the published measurements,
but split the original combined host scripts so there is one independently runnable measurement
per file. Every file begins by stating what it tests, why the test matters, and where it writes
its result.

They default to the environment of record (`H=/home/fkautz`, patched runsc under `$H/gvisor`,
and the expected bundle under `$H`). Override those paths without editing a runner:

```
H="$HOME" RUNSC="$HOME/gvisor/bazel-bin/runsc/runsc_/runsc" \
  BUNDLE="$HOME/adkbundle" ./runs/test-kvm-cold-vs-restore.sh
```

Setup and smoke test:

| script | explicitly does | why it matters |
| --- | --- | --- |
| `runs/build-adk-bundle.sh` | builds and exports the instrumented google-adk OCI bundle | fixes the real workload used by the ADK measurements |
| `runs/test-gvisor-pgalloc.sh` | runs the patched pgalloc unit suite | validates mapping and base/delta behavior before runsc tests |
| `runs/test-linux-cow-primitive.sh` | compiles/runs the host MAP_PRIVATE smoke test | proves the kernel primitive independently of gVisor |
| `runs/test-uffd-populate-latency.sh` | measures 2 MiB userfaultfd population | isolates the lazy-loading mechanism floor |
| `runs/test-eager-verify-before-expose.sh` | verifies, shares, and tampers with a Terrapin-addressed base | proves eager integrity and density compose |
| `runs/test-lazy-verify-before-expose.sh` | lazily fetches/verifies base blocks through userfaultfd | proves lazy integrity, rejection, and sharing compose |
| `runs/test-adk-smoke.sh` | boots one ADK agent under runsc/KVM and checks its output/listing | fails early if the bundle or runtime is unusable |

Measurements and their corresponding findings:

| script | measures | finding | headline |
| --- | --- | --- | --- |
| `runs/test-kvm-live-flatten.sh` | eight-clone flatten over a 256 MiB touch-all shared base | §13b | 5.7x at N=8 (runsc, KVM) |
| `runs/test-kvm-density-ceiling.sh` | density ceiling, escalating N=32 through 600 | §13c | ~300 sandboxes / 15 GiB |
| `runs/test-sandbox-memory-floor.sh` | sentry/gofer PSS/RSS and memory classification | §13d | ~20 MiB, ~half Go runtime |
| `runs/test-sandbox-thread-floor.sh` | sentry/gofer thread and memory dimensions | §13d | the floor includes structural runtime state |
| `runs/test-go-runtime-tuning.sh` | four GOGC/GOMEMLIMIT configurations | §13d | tuning is a no-op for the structural floor |
| `runs/test-kvm-cold-vs-restore.sh` | one KVM cold start versus one shared-base restore | §13d | 11.4s -> 0.6s |
| `runs/test-kvm-concurrent-restore.sh` | eight concurrent KVM restores | §13d | reports the slowest time-to-service |
| `runs/test-restore-warmth.sh` | first five post-restore durations versus steady state | §13e | restored execution remains warm |
| `runs/test-restore-correctness.sh` | response validation and tick/state continuity | §13e | state survives bit-correctly |
| `runs/test-clone-independence.sh` | three clones advancing from one checkpoint | §13e | mutable state is clone-local |
| `runs/test-restore-burst.sh` | self-contained bursts of 50 and 100 restores | §13e | CPU-throughput-bound |
| `runs/test-checkpoint-storage.sh` | shared-base versus normal checkpoint storage/time | §13f | ~248 KiB parked cost per agent |
| `runs/test-systrap-cold-vs-restore.sh` | cold versus restore latency without KVM | §13f | latency result holds on systrap |
| `runs/test-adk-density.sh` | eight real Python/google-adk clones | §13d | 2.9x (floor-limited) |
| `runs/test-adk-resident-working-set.sh` | 1 GiB cold padding at 8 and 16 clones | §13d | allocation does not imply residency |

`runs/build-adk-bundle.sh` builds from `agent/agent.py` (the instrumented mock-LLM agent;
`agent-basic.py` is the minimal variant). The workloads are `../cr_workload.c` (C
checkpoint/restore workload) and `agent/` (Python google-adk 2.3.0, Python 3.12, mocked LLM,
no network).

## 5. Expected numbers and environment

Full results and the honest caveats are in `../density-prototype-findings.md` (§§13a-13f).
Environment of record: GCE `n2-standard-4`, Ubuntu 24.04, nested virtualization; gVisor
`928199eb9`; bazel 8.3.1; google-adk 2.3.0 on Python 3.12.

## Related code (parent directory)

- `../gvisor3-s1.pgalloc.patch`, `../c1-restore-plumbing.patch`, and
  `../c1b-checkpoint-plumbing.patch` are experimental development records. They overlap and are not
  the release series; use `patches/gvisor-shared-base-v1/` for reproduction.
- `../sharedbase_test.go`, `../saverestore_test.go`, `../flatten_test.go` -- the in-tree pgalloc
  tests (also contained in the combined patch).
- `../density_smoke.c` -- the standalone Linux `MAP_PRIVATE` copy-on-write smoke test (§ "does the
  OS even do this").
- `../verify_share/`, `../lazy_verify/` -- Terrapin verify-before-expose, eager and lazy (uffd+CAS).
- `../rhz.go`, `../memsnap.go`, `../pagediff.go` -- restore-hazard and memory-delta measurement.
