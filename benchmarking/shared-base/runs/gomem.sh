#!/bin/bash
H=/home/fkautz
R=$H/gvisor/bazel-bin/runsc/runsc_/runsc
B=$H/c1test/bundle
pkill -f runsc-sandbox 2>/dev/null; sleep 1
sed -i "s/WARM_MB=[0-9]*/WARM_MB=4/" $B/config.json
ROOT="--root /run/gomem --platform=kvm --ignore-cgroups --network=none"
$R $ROOT run -bundle $B g1 > $H/g1.log 2>&1 &
sleep 7
> $H/gomem.txt
dump(){ awk "/^Rss:/{rss=\$2}/^Pss:/{pss=\$2}/^Shared_Clean:/{sc=\$2}/^Private_Dirty:/{pd=\$2}/^Private_Clean:/{pc=\$2}/^Anonymous:/{an=\$2}END{printf \"Pss=%dM Rss=%dM  Anon(Go-heap/stacks)=%dM PrivDirty=%dM PrivClean=%dM SharedClean(binary)=%dM\n\", pss/1024,rss/1024,an/1024,pd/1024,pc/1024,sc/1024}" /proc/$1/smaps_rollup 2>/dev/null; }
for pid in $(pgrep -f "runsc-sandbox.*root=/run/gomem"); do echo "SENTRY: $(dump $pid)" >> $H/gomem.txt; done
for pid in $(pgrep -f "gofer.*root=/run/gomem"); do echo "GOFER:  $(dump $pid)" >> $H/gomem.txt; done
echo -n "sentry GOGC/GOMEMLIMIT: " >> $H/gomem.txt
for pid in $(pgrep -f "runsc-sandbox.*root=/run/gomem"); do tr "\0" "\n" < /proc/$pid/environ 2>/dev/null | grep -iE "GOGC|GOMEMLIMIT" >> $H/gomem.txt || echo "(unset -> Go default GOGC=100)" >> $H/gomem.txt; done
# count processes per sandbox
echo "procs for one sandbox: sentry=$(pgrep -fc "runsc-sandbox.*root=/run/gomem") gofer=$(pgrep -fc "gofer.*root=/run/gomem") other-runsc=$(pgrep -fc "runsc.*root=/run/gomem")" >> $H/gomem.txt
$R $ROOT kill g1 KILL 2>/dev/null; $R $ROOT delete --force g1 2>/dev/null
echo "=== DONE ===" >> $H/gomem.txt
