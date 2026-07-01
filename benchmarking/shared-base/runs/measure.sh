#!/bin/bash
H=/home/fkautz
R=$H/gvisor/bazel-bin/runsc/runsc_/runsc
B=$H/c1test/bundle; D=$H/c1b2; rm -rf $D; mkdir -p $D/ckpt
pkill -f runsc-sandbox 2>/dev/null; sleep 1
grep -q "p += 64" $H/cr_workload.c && sed -i "s/p += 64/p += 1/g" $H/cr_workload.c
gcc -O2 -static -o $B/rootfs/cr $H/cr_workload.c
sed -i "s/WARM_MB=[0-9]*/WARM_MB=256/" $B/config.json
ROOT="--root /run/c1b2 --platform=kvm --ignore-cgroups --network=none"
$R $ROOT run -bundle $B s2 > $H/s2.log 2>&1 &
sleep 10
$R $ROOT checkpoint --shared-base --image-path=$D/ckpt s2 >/dev/null 2>&1
for i in $(seq 1 8); do $R $ROOT restore --image-path=$D/ckpt -bundle $B k$i > $H/k$i.log 2>&1 & done
sleep 24
{
  echo "base.img=$(du -h $D/ckpt/base.img 2>/dev/null|cut -f1)  pages.img=$(du -h $D/ckpt/pages.img 2>/dev/null|cut -f1) (delta)"
  echo "ref: $(tail -1 $H/s2.log)"
  for i in 1 4 8; do echo "k$i: $(tail -1 $H/k$i.log)"; done
  trss=0;tpss=0;n=0
  for pid in $(pgrep -f "runsc-sandbox.*root=/run/c1b2"); do
    r=$(awk "/^Rss:/{print \$2}" /proc/$pid/smaps_rollup 2>/dev/null)
    p=$(awk "/^Pss:/{print \$2}" /proc/$pid/smaps_rollup 2>/dev/null)
    [ -n "$r" ] && { trss=$((trss+r));tpss=$((tpss+p));n=$((n+1)); }
  done
  echo "N=$n sentries: sumRss=$((trss/1024))MiB (no-share)  sumPss=$((tpss/1024))MiB (shared)"
  [ $tpss -gt 0 ] && echo "FLATTEN through runsc on live KVM = $(echo "scale=2;$trss/$tpss"|bc)x"
} > $H/flatten_result.txt 2>&1
for i in $(seq 1 8); do $R $ROOT kill k$i KILL 2>/dev/null; $R $ROOT delete --force k$i 2>/dev/null; done
echo "=== DONE ===" >> $H/flatten_result.txt
