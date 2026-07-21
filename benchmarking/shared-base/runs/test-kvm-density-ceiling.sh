#!/usr/bin/env bash
# Test: Restore progressively larger KVM clone populations (32 through 600) until host memory headroom reaches 1.2 GiB.
# Why: This finds the practical sandbox density ceiling rather than extrapolating from a small clone count.
# Output: $H/ceiling.txt
H=${H:-/home/fkautz}
R=${RUNSC:-$H/gvisor/bazel-bin/runsc/runsc_/runsc}
B=$H/c1test/bundle; D=$H/c1c; rm -rf $D $H/a*.log; mkdir -p $D/ckpt
pkill -f runsc-sandbox 2>/dev/null; sleep 2
grep -q "p += 1" $H/cr_workload.c && sed -i "s/p += 1/p += 64/g" $H/cr_workload.c
gcc -O2 -static -o $B/rootfs/cr $H/cr_workload.c
sed -i "s/WARM_MB=[0-9]*/WARM_MB=128/" $B/config.json
ROOT="--root /run/c1c --platform=kvm --ignore-cgroups --network=none"
> $H/ceiling.txt
$R $ROOT run -bundle $B seed > $H/seed.log 2>&1 &
sleep 9
$R $ROOT checkpoint --shared-base --image-path=$D/ckpt seed >/dev/null 2>&1
echo "base.img=$(du -h $D/ckpt/base.img|cut -f1) pages.img=$(du -h $D/ckpt/pages.img|cut -f1) (delta)  vCPU=$(nproc) RAM=$(free -m|awk "/Mem:/{print \$2}")MiB" >> $H/ceiling.txt
idx=0
for N in 32 64 128 200 300 400 500 600; do
  while [ $idx -lt $N ]; do
    idx=$((idx+1))
    $R $ROOT restore --image-path=$D/ckpt -bundle $B a$idx > $H/a$idx.log 2>&1 &
    [ $((idx % 25)) -eq 0 ] && sleep 0.4
  done
  sleep 20
  running=$(pgrep -fc "runsc-sandbox.*root=/run/c1c")
  memavail=$(awk "/MemAvailable/{print int(\$2/1024)}" /proc/meminfo)
  load=$(cut -d" " -f1 /proc/loadavg)
  ok=$(grep -l "checksum=ok" $H/a*.log 2>/dev/null | wc -l)
  trss=0; tpss=0
  for pid in $(pgrep -f "runsc-sandbox.*root=/run/c1c"); do
    read r p < <(awk "/^Rss:/{rss=\$2}/^Pss:/{pss=\$2}END{print rss,pss}" /proc/$pid/smaps_rollup 2>/dev/null)
    [ -n "$r" ] && { trss=$((trss+r)); tpss=$((tpss+p)); }
  done
  echo "N=$N running=$running ticking_ok=$ok sumRss=$((trss/1024))MiB sumPss=$((tpss/1024))MiB MemAvail=${memavail}MiB load1=$load" >> $H/ceiling.txt
  if [ "$memavail" -lt 1200 ]; then echo "STOP: MemAvailable ${memavail}MiB below 1200MiB headroom at N=$N" >> $H/ceiling.txt; break; fi
done
echo "=== DONE ===" >> $H/ceiling.txt
