// uffd_latency.c -- measure the userfaultfd cold-fault POPULATE latency per
// 2 MiB block (fault trap -> handler -> UFFDIO_COPY 2 MiB from a local base ->
// resume). This is the mechanism floor of verify-before-expose; the Terrapin
// hash cost (measured separately) is added on top for the full cold-fault cost.
//
// Build: gcc -O2 -pthread -o uffd_latency uffd_latency.c

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
#include <sys/syscall.h>
#include <time.h>
#include <unistd.h>
#include <sys/mman.h>

#define BLOCK (2 * 1024 * 1024UL)

static long  uffd;
static char *region, *base;
static size_t rlen;

static void *handler(void *arg) {
    (void)arg;
    for (;;) {
        struct pollfd p = { .fd = uffd, .events = POLLIN };
        if (poll(&p, 1, -1) < 0) break;
        struct uffd_msg msg;
        if (read(uffd, &msg, sizeof msg) <= 0) break;
        if (msg.event != UFFD_EVENT_PAGEFAULT) continue;
        unsigned long addr = msg.arg.pagefault.address;
        unsigned long boff = (addr - (unsigned long)region) & ~(BLOCK - 1);
        struct uffdio_copy c = {
            .dst = (unsigned long)region + boff,
            .src = (unsigned long)base + boff,
            .len = BLOCK, .mode = 0 };
        if (ioctl(uffd, UFFDIO_COPY, &c) < 0) { perror("UFFDIO_COPY"); }
    }
    return NULL;
}

static int cmp(const void *a, const void *b) {
    double x = *(const double *)a, y = *(const double *)b;
    return (x > y) - (x < y);
}

int main(int argc, char **argv) {
    size_t mb = (argc > 1) ? (size_t)atol(argv[1]) : 512;
    rlen = mb * 1024 * 1024;
    size_t nblk = rlen / BLOCK;

    base = mmap(NULL, rlen, PROT_READ | PROT_WRITE, MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
    for (size_t o = 0; o < rlen; o += 4096) base[o] = (char)(o >> 12);  // touch base
    region = mmap(NULL, rlen, PROT_READ | PROT_WRITE, MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
    if (base == MAP_FAILED || region == MAP_FAILED) { perror("mmap"); return 1; }

    uffd = syscall(SYS_userfaultfd, O_CLOEXEC | O_NONBLOCK);
    struct uffdio_api api = { .api = UFFD_API };
    if (ioctl(uffd, UFFDIO_API, &api) < 0) { perror("UFFDIO_API"); return 1; }
    struct uffdio_register reg = {
        .range = { .start = (unsigned long)region, .len = rlen },
        .mode = UFFDIO_REGISTER_MODE_MISSING };
    if (ioctl(uffd, UFFDIO_REGISTER, &reg) < 0) { perror("UFFDIO_REGISTER"); return 1; }
    pthread_t th; pthread_create(&th, NULL, handler, NULL);

    double *lat = malloc(nblk * sizeof(double));
    volatile char sink = 0;
    for (size_t b = 0; b < nblk; b++) {
        struct timespec t0, t1;
        clock_gettime(CLOCK_MONOTONIC, &t0);
        sink ^= region[b * BLOCK];           // cold touch -> MISSING fault -> populate
        clock_gettime(CLOCK_MONOTONIC, &t1);
        lat[b] = (t1.tv_sec - t0.tv_sec) * 1e6 + (t1.tv_nsec - t0.tv_nsec) / 1e3; // us
    }
    (void)sink;

    qsort(lat, nblk, sizeof(double), cmp);
    double sum = 0; for (size_t b = 0; b < nblk; b++) sum += lat[b];
    printf("userfaultfd populate per 2 MiB block (local base, n=%zu blocks, %zuMiB):\n", nblk, mb);
    printf("  p50=%.1f us  p99=%.1f us  max=%.1f us  mean=%.1f us\n",
           lat[nblk/2], lat[(size_t)(nblk*0.99)], lat[nblk-1], sum/nblk);
    printf("  populate throughput = %.2f GB/s\n", (double)rlen / (sum/1e6) / 1e9);
    return 0;
}
