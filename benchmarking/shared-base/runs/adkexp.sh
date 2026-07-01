#!/bin/bash
H=/home/fkautz
R=$H/gvisor/bazel-bin/runsc/runsc_/runsc
B=$H/adkbundle
D=$H/adkck; rm -rf $D $H/a*.log; mkdir -p $D/ckpt
pkill -f "runsc-sandbox" 2>/dev/null; sleep 2
ROOT="--root /run/adk2 --platform=kvm --ignore-cgroups --network=none"
> $H/adkexp.txt
$R $ROOT run -bundle $B seed > $H/seed.log 2>&1 &
sleep 34
echo "seed warm: $(grep -vE "token usage" $H/seed.log | tail -1)" >> $H/adkexp.txt
$R $ROOT checkpoint --shared-base --image-path=$D/ckpt seed >/dev/null 2>&1 && echo "checkpoint --shared-base OK" >> $H/adkexp.txt || echo "CHECKPOINT FAILED (see seed.log)" >> $H/adkexp.txt
echo "base.img=$(du -h $D/ckpt/base.img 2>/dev/null|cut -f1)  pages.img(delta)=$(du -h $D/ckpt/pages.img 2>/dev/null|cut -f1)" >> $H/adkexp.txt
N=8
for i in $(seq 1 $N); do $R $ROOT restore --image-path=$D/ckpt -bundle $B a$i > $H/a$i.log 2>&1 & done
sleep 32
ok=0; for i in $(seq 1 $N); do grep -q "tick=" $H/a$i.log && ok=$((ok+1)); done
echo "clones restored+ticking: $ok/$N   (sample: $(grep -vE "token usage" $H/a1.log | tail -1))" >> $H/adkexp.txt
trss=0;tpss=0;n=0
for pid in $(pgrep -f "runsc-sandbox.*root=/run/adk2"); do
  read r p < <(awk "/^Rss:/{rss=\$2}/^Pss:/{pss=\$2}END{print rss,pss}" /proc/$pid/smaps_rollup 2>/dev/null)
  [ -n "$r" ] && { trss=$((trss+r));tpss=$((tpss+p));n=$((n+1)); }
done
memavail=$(awk "/MemAvailable/{print int(\$2/1024)}" /proc/meminfo)
echo "N=$n sentries: sumRss=$((trss/1024))M (no-share)  sumPss=$((tpss/1024))M (shared)  MemAvail=${memavail}M" >> $H/adkexp.txt
[ $tpss -gt 0 ] && echo "FLATTEN (real Python+ADK base) = $(echo "scale=2;$trss/$tpss"|bc)x   per-clone Pss=$((tpss/1024/n))M" >> $H/adkexp.txt
for i in $(seq 1 $N); do $R $ROOT kill a$i KILL 2>/dev/null; $R $ROOT delete --force a$i 2>/dev/null; done
echo "=== DONE ===" >> $H/adkexp.txt
