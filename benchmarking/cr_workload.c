// cr_workload.c -- a "warmed" stateful workload for runsc checkpoint/restore.
//
// Allocates WARM_MB of heap, fills it with a verifiable per-page pattern, then
// every second increments a counter held in memory and prints
//   tick=<n> warm=<MiB> checksum=<ok|BAD>
// to stdout. If a checkpoint+restore preserves live memory and execution state,
// the tick must CONTINUE from where it was checkpointed (not reset to 1) and the
// checksum must stay ok. Build static so the bundle needs no shared libs:
//   gcc -O2 -static -o cr_workload cr_workload.c

#define _GNU_SOURCE
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

static long PAGE;

static uint8_t pat(size_t page, size_t off) {
    return (uint8_t)((page * 1099511628211ULL + off) & 0xff);
}

int main(void) {
    PAGE = sysconf(_SC_PAGESIZE);
    setvbuf(stdout, NULL, _IONBF, 0);
    size_t mb = 1024;
    const char *e = getenv("WARM_MB");
    if (e && atol(e) > 0) mb = (size_t)atol(e);
    size_t n = mb * 1024 * 1024;
    size_t pages = n / PAGE;

    uint8_t *buf = malloc(n);
    if (!buf) { fprintf(stderr, "malloc %zuMiB failed\n", mb); return 1; }
    for (size_t p = 0; p < pages; p++)
        for (size_t o = 0; o < (size_t)PAGE; o++)
            buf[p * PAGE + o] = pat(p, o);

    // Reference checksum over a sample of pages (every 64th page, first 64 bytes).
    uint64_t ref = 0;
    for (size_t p = 0; p < pages; p += 64)
        for (size_t o = 0; o < 64; o++) ref += buf[p * PAGE + o];

    printf("ready warm=%zuMiB pages=%zu pid=%d\n", mb, pages, getpid());

    uint64_t tick = 0;
    for (;;) {
        sleep(1);
        tick++;
        uint64_t sum = 0;
        for (size_t p = 0; p < pages; p += 64)
            for (size_t o = 0; o < 64; o++) sum += buf[p * PAGE + o];
        printf("tick=%llu warm=%zuMiB checksum=%s\n",
               (unsigned long long)tick, mb, sum == ref ? "ok" : "BAD");
    }
    return 0;
}
