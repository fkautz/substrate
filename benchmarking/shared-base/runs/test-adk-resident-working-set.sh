#!/usr/bin/env bash
# Test: Pad an ADK process with 1 GiB of allocated-but-cold memory, then compare RSS/PSS at 8 and 16 restored clones.
# Why: This separates virtual allocation size from resident working set and shows that untouched padding does not consume proportional RAM.
# Output: $H/adkexp2.txt
H=${H:-/home/fkautz}
R=${RUNSC:-$H/gvisor/bazel-bin/runsc/runsc_/runsc}
B=${BUNDLE:-$H/adkbundle}
D=$H/adkck2; rm -rf $D $H/b*.log; mkdir -p $D/ckpt
pkill -f "runsc-sandbox" 2>/dev/null; sleep 2
# pad the agent warm base to ~1 GiB (simulate a large loaded working set)
python3 -c "import json;c=json.load(open(\"$B/config.json\"));e=[x for x in c[\"process\"][\"env\"] if not x.startswith(\"PAD_MB=\")]+[\"PAD_MB=1024\"];c[\"process\"][\"env\"]=e;json.dump(c,open(\"$B/config.json\",\"w\"),indent=2)"
ROOT="--root /run/adk3 --platform=kvm --ignore-cgroups --network=none"
> $H/adkexp2.txt
$R $ROOT run -bundle $B seed > $H/seedb.log 2>&1 &
sleep 46
echo "seed warm: $(grep -vE "token usage" $H/seedb.log | tail -1)" >> $H/adkexp2.txt
$R $ROOT checkpoint --shared-base --image-path=$D/ckpt seed >/dev/null 2>&1 && echo "checkpoint --shared-base OK" >> $H/adkexp2.txt || echo "CHECKPOINT FAILED" >> $H/adkexp2.txt
echo "base.img=$(du -h $D/ckpt/base.img 2>/dev/null|cut -f1)  pages.img(delta)=$(du -h $D/ckpt/pages.img 2>/dev/null|cut -f1)" >> $H/adkexp2.txt
measure(){
  local tag=$1
  local trss=0 tpss=0 n=0
  for pid in $(pgrep -f "runsc-sandbox.*root=/run/adk3"); do
    read r p < <(awk "/^Rss:/{rss=\$2}/^Pss:/{pss=\$2}END{print rss,pss}" /proc/$pid/smaps_rollup 2>/dev/null)
    [ -n "$r" ] && { trss=$((trss+r));tpss=$((tpss+p));n=$((n+1)); }
  done
  local ma=$(awk "/MemAvailable/{print int(\$2/1024)}" /proc/meminfo)
  echo "$tag N=$n: sumRss=$((trss/1024))M sumPss=$((tpss/1024))M MemAvail=${ma}M FLATTEN=$(echo "scale=2;$trss/$tpss"|bc)x" >> $H/adkexp2.txt
}
for i in $(seq 1 8); do $R $ROOT restore --image-path=$D/ckpt -bundle $B b$i > $H/b$i.log 2>&1 & done
sleep 34
ok=0; for i in $(seq 1 8); do grep -q "tick=" $H/b$i.log && ok=$((ok+1)); done
echo "clones ticking: $ok/8" >> $H/adkexp2.txt
measure "@8clones"
for i in $(seq 9 16); do $R $ROOT restore --image-path=$D/ckpt -bundle $B b$i > $H/b$i.log 2>&1 & done
sleep 34
measure "@16clones"
for i in $(seq 1 16); do $R $ROOT kill b$i KILL 2>/dev/null; $R $ROOT delete --force b$i 2>/dev/null; done
echo "=== DONE ===" >> $H/adkexp2.txt
