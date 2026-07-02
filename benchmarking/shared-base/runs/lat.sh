#!/bin/bash
H=/home/fkautz
R=$H/gvisor/bazel-bin/runsc/runsc_/runsc
B=$H/adkbundle
D=$H/lat; rm -rf $D $H/cold.log $H/warm.log $H/b*.log; mkdir -p $D/ckpt
pkill -f runsc-sandbox 2>/dev/null; sleep 2
python3 -c "import json;c=json.load(open(\"$B/config.json\"));e=[x for x in c[\"process\"][\"env\"] if not x.startswith(\"PAD_MB=\")]+[\"PAD_MB=0\"];c[\"process\"][\"env\"]=e;json.dump(c,open(\"$B/config.json\",\"w\"),indent=2)"
sed -i "s/time.sleep([0-9.]*)/time.sleep(0.25)/" $B/rootfs/agent.py
ROOT="--root /run/lat --platform=kvm --ignore-cgroups --network=none"
CLK=$(getconf CLK_TCK)
cpu_of(){ local pat="$1" t=0 st rest; for pid in $(pgrep -f "$pat"); do st=$(cat /proc/$pid/stat 2>/dev/null); [ -z "$st" ] && continue; rest=${st#*) }; set -- $rest; t=$((t+${12}+${13})); done; echo "scale=2;$t/$CLK"|bc; }
> $H/lat.txt
# ---------- COLD START ----------
t0=$(date +%s.%N)
$R $ROOT run -bundle $B cold > $H/cold.log 2>&1 &
until grep -q "ready adk-agent" $H/cold.log 2>/dev/null; do sleep 0.05; done
t1=$(date +%s.%N)
sleep 0.3; ccpu=$(cpu_of "runsc.*root=/run/lat")
echo "COLD START (runsc run -> agent ready): wall=$(echo "$t1-$t0"|bc)s  cpu=${ccpu}s" >> $H/lat.txt
$R $ROOT checkpoint --shared-base --image-path=$D/ckpt cold >/dev/null 2>&1
$R $ROOT delete --force cold 2>/dev/null; pkill -f "runsc-sandbox.*root=/run/lat" 2>/dev/null; sleep 2
echo "checkpoint base.img=$(du -h $D/ckpt/base.img|cut -f1) delta=$(du -h $D/ckpt/pages.img|cut -f1)" >> $H/lat.txt
# ---------- RESTORE (single) ----------
t0=$(date +%s.%N)
$R $ROOT restore --image-path=$D/ckpt -bundle $B warm > $H/warm.log 2>&1 &
until grep -q "tick=" $H/warm.log 2>/dev/null; do sleep 0.02; done
t1=$(date +%s.%N)
sleep 0.3; rcpu=$(cpu_of "runsc.*root=/run/lat")
echo "RESTORE single (runsc restore -> agent serving): wall=$(echo "$t1-$t0"|bc)s  cpu=${rcpu}s" >> $H/lat.txt
$R $ROOT delete --force warm 2>/dev/null; pkill -f "runsc-sandbox.*root=/run/lat" 2>/dev/null; sleep 2
# ---------- RESTORE (8 concurrent) -> slowest time-to-serve ----------
t0=$(date +%s.%N)
for i in $(seq 1 8); do $R $ROOT restore --image-path=$D/ckpt -bundle $B b$i > $H/b$i.log 2>&1 & done
done=0; declare -A rt
while [ $done -lt 8 ]; do
  for i in $(seq 1 8); do [ -z "${rt[$i]}" ] && grep -q "tick=" $H/b$i.log 2>/dev/null && { rt[$i]=$(date +%s.%N); done=$((done+1)); }; done
  sleep 0.03
done
mx=0; for i in $(seq 1 8); do e=$(echo "${rt[$i]}-$t0"|bc); (( $(echo "$e>$mx"|bc) )) && mx=$e; done
echo "RESTORE 8 concurrent: slowest agent serving at wall=${mx}s" >> $H/lat.txt
for i in $(seq 1 8); do $R $ROOT kill b$i KILL 2>/dev/null; $R $ROOT delete --force b$i 2>/dev/null; done
echo "=== DONE ===" >> $H/lat.txt
