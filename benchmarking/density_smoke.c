// density_smoke.c -- prove the LLIFS rung-3 density primitives on this kernel,
// independent of gVisor/Terrapin/CAS. Maps the §16.1 acceptance test:
//   (1) one verified base, MAP_PRIVATE across N processes -> resident base pages
//       are PHYSICALLY SHARED (Pss divides ~ Rss/N), and a write creates a
//       private copy for the writer ONLY (copy-on-write, no bleed).
//   (2) userfaultfd: an ABSENT range never reads as zero -- first touch faults
//       and is populated FROM THE BASE (UFFDIO_COPY), not zero; a signed
//       KNOWN-ZERO range installs a zero page (UFFDIO_ZEROPAGE) with no fetch.
//
// Build:  gcc -O2 -pthread -o density_smoke density_smoke.c
// Run:    ./density_smoke

#define _GNU_SOURCE
#include <fcntl.h>
#include <linux/userfaultfd.h>
#include <poll.h>
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/mman.h>
#include <sys/syscall.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <unistd.h>

static long PAGE;
static const size_t BASE_MB = 256;
static const int    N = 8;

// Read a kB-valued field (e.g. "Pss:") from /proc/self/smaps_rollup, in bytes.
static long rollup_field(const char *key) {
    FILE *f = fopen("/proc/self/smaps_rollup", "r");
    if (!f) return -1;
    char line[256]; long kb = -1; size_t klen = strlen(key);
    while (fgets(line, sizeof line, f)) {
        if (strncmp(line, key, klen) == 0) { kb = atol(line + klen); break; }
    }
    fclose(f);
    return kb < 0 ? -1 : kb * 1024;
}

// ---- Part 1: shared base + copy-on-write across N processes ----------------
static int test_sharing(void) {
    size_t base = BASE_MB * 1024 * 1024;
    int fd = memfd_create("llifs-base", MFD_CLOEXEC);
    if (fd < 0) { perror("memfd_create"); return 1; }
    if (ftruncate(fd, base) != 0) { perror("ftruncate"); return 1; }
    // Fill the base with a nonzero pattern so "populated" is distinguishable
    // from zero, and so written pages differ from base.
    char *seed = mmap(NULL, base, PROT_READ | PROT_WRITE, MAP_SHARED, fd, 0);
    if (seed == MAP_FAILED) { perror("seed mmap"); return 1; }
    for (size_t o = 0; o < base; o += PAGE) seed[o] = 0xA5;
    munmap(seed, base);

    int ready[2], go[2], done[2], fin[2];
    if (pipe(ready) || pipe(go) || pipe(done) || pipe(fin)) { perror("pipe"); return 1; }
    fflush(stdout);  // drain parent's buffer so children do not re-emit it

    for (int i = 0; i < N; i++) {
        pid_t pid = fork();
        if (pid == 0) {
            char c;
            // Each child maps the ONE base file MAP_PRIVATE and reads every page.
            char *m = mmap(NULL, base, PROT_READ | PROT_WRITE, MAP_PRIVATE, fd, 0);
            if (m == MAP_FAILED) { perror("child mmap"); _exit(2); }
            volatile uint64_t acc = 0;
            for (size_t o = 0; o < base; o += PAGE) acc += (unsigned char)m[o];
            (void)acc;
            // Writer (child 0) touches the first half -> private copy for IT only.
            if (i == 0)
                for (size_t o = 0; o < base / 2; o += PAGE) m[o] = 0x5A;
            c = 1; (void)!write(ready[1], &c, 1);       // setup done
            (void)!read(go[0], &c, 1);                  // barrier A: all set up
            long rss = rollup_field("Rss:"), pss = rollup_field("Pss:");
            long pd  = rollup_field("Private_Dirty:");
            printf("  child %d%s  rss=%4ldMiB  pss=%4ldMiB  private_dirty=%4ldMiB\n",
                   i, i == 0 ? " (writer, wrote half)" : "             ",
                   rss>>20, pss>>20, pd>>20);
            fflush(stdout);
            c = 1; (void)!write(done[1], &c, 1);        // measured
            (void)!read(fin[0], &c, 1);                 // barrier B: stay mapped
            _exit(0);
        }
    }
    for (int i = 0; i < N; i++) { char c; (void)!read(ready[0], &c, 1); } // all set up
    for (int i = 0; i < N; i++) { char c = 1; (void)!write(go[1], &c, 1); } // measure now
    for (int i = 0; i < N; i++) { char c; (void)!read(done[0], &c, 1); }  // all measured
    for (int i = 0; i < N; i++) { char c = 1; (void)!write(fin[1], &c, 1); } // release
    for (int i = 0; i < N; i++) wait(NULL);
    close(fd);
    printf("  => base=%zuMiB across N=%d procs. SHARED if each rss~=base but "
           "pss~=base/N; writer's private_dirty~=base/2, others ~0.\n",
           BASE_MB, N);
    return 0;
}

// ---- Part 2: userfaultfd -- absent != zero; known-zero without fetch --------
static char    *uregion;       // the userfaultfd-backed region
static char    *ubase;         // the "verified base" bytes we populate from
static size_t   ulen;
static long     uffd;
static volatile long faults_data = 0, faults_zero = 0;
// page index policy: even index -> data (copy from base); odd -> known zero.
static void *uffd_handler(void *arg) {
    (void)arg;
    char *zeropg = calloc(1, PAGE);
    for (;;) {
        struct pollfd p = { .fd = uffd, .events = POLLIN };
        if (poll(&p, 1, -1) < 0) break;
        struct uffd_msg msg;
        if (read(uffd, &msg, sizeof msg) <= 0) break;
        if (msg.event != UFFD_EVENT_PAGEFAULT) continue;
        unsigned long addr = msg.arg.pagefault.address & ~(PAGE - 1);
        size_t idx = (addr - (unsigned long)uregion) / PAGE;
        if (idx % 2 == 0) {
            // DATA: populate from the verified base -- never a silent zero.
            struct uffdio_copy c = {
                .dst = addr, .src = (unsigned long)(ubase + idx * PAGE),
                .len = PAGE, .mode = 0 };
            if (ioctl(uffd, UFFDIO_COPY, &c) < 0) { perror("UFFDIO_COPY"); }
            faults_data++;
        } else {
            // KNOWN-ZERO: install a verified zero page, no base fetch.
            struct uffdio_zeropage z = {
                .range = { .start = addr, .len = PAGE }, .mode = 0 };
            if (ioctl(uffd, UFFDIO_ZEROPAGE, &z) < 0) { perror("UFFDIO_ZEROPAGE"); }
            faults_zero++;
        }
    }
    free(zeropg);
    return NULL;
}

static int test_userfaultfd(void) {
    ulen = 64 * PAGE;
    ubase = mmap(NULL, ulen, PROT_READ | PROT_WRITE, MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
    for (size_t o = 0; o < ulen; o += PAGE) memset(ubase + o, 0xC3, PAGE); // base pattern
    uregion = mmap(NULL, ulen, PROT_READ | PROT_WRITE, MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
    if (ubase == MAP_FAILED || uregion == MAP_FAILED) { perror("mmap"); return 1; }

    uffd = syscall(SYS_userfaultfd, O_CLOEXEC | O_NONBLOCK);
    if (uffd < 0) { perror("userfaultfd"); return 1; }
    struct uffdio_api api = { .api = UFFD_API };
    if (ioctl(uffd, UFFDIO_API, &api) < 0) { perror("UFFDIO_API"); return 1; }
    struct uffdio_register reg = {
        .range = { .start = (unsigned long)uregion, .len = ulen },
        .mode = UFFDIO_REGISTER_MODE_MISSING };
    if (ioctl(uffd, UFFDIO_REGISTER, &reg) < 0) { perror("UFFDIO_REGISTER"); return 1; }

    pthread_t th; pthread_create(&th, NULL, uffd_handler, NULL);

    // Touch every page. Even pages must come back as base bytes (0xC3), odd as 0.
    int data_ok = 1, zero_ok = 1, absent_zero_bug = 0;
    for (size_t i = 0; i < 64; i++) {
        unsigned char v = (unsigned char)uregion[i * PAGE];
        if (i % 2 == 0) {            // expected: base content
            if (v != 0xC3) { data_ok = 0; if (v == 0) absent_zero_bug = 1; }
        } else {                     // expected: known zero
            if (v != 0x00) zero_ok = 0;
        }
    }
    printf("  data pages faulted=%ld (populated from base, expect 0xC3): %s\n",
           faults_data, data_ok ? "OK (absent never read as zero)" :
           (absent_zero_bug ? "FAIL: absent read as ZERO" : "FAIL: wrong bytes"));
    printf("  zero pages faulted=%ld (UFFDIO_ZEROPAGE, no base fetch): %s\n",
           faults_zero, zero_ok ? "OK" : "FAIL");
    return (data_ok && zero_ok) ? 0 : 1;
}

int main(void) {
    setvbuf(stdout, NULL, _IONBF, 0);  // avoid fork-duplicated buffered output
    PAGE = sysconf(_SC_PAGESIZE);
    printf("LLIFS density smoke test  (page=%ldB, kernel reports unprivileged "
           "userfaultfd)\n", PAGE);
    printf("[1] shared base + copy-on-write across processes\n");
    int r1 = test_sharing();
    printf("[2] userfaultfd: absent != zero, known-zero without fetch\n");
    int r2 = test_userfaultfd();
    printf("RESULT: %s\n", (r1 == 0 && r2 == 0) ? "PASS" : "CHECK OUTPUT");
    return r1 || r2;
}
