#!/usr/bin/env bash
# Test: Run the same small KVM sandbox under four GOGC/GOMEMLIMIT configurations and measure sentry private memory.
# Why: This checks whether the density floor is reclaimable Go heap or structural runtime state.
# Output: $H/gotune.txt
H=${H:-/home/fkautz}
R=${RUNSC:-$H/gvisor/bazel-bin/runsc/runsc_/runsc}
B=${BUNDLE:-$H/c1test/bundle}
sed -i "s/WARM_MB=[0-9]*/WARM_MB=4/" $B/config.json
> $H/gotune.txt
sentry_priv(){ awk "/^Pss:/{pss=\$2}/^Private_Dirty:/{pd=\$2}/^Private_Clean:/{pc=\$2}/^Anonymous:/{an=\$2}END{printf \"Pss=%dM Anon(Go)=%dM Priv(marginal)=%dM\", pss/1024,an/1024,(pd+pc)/1024}" /proc/$1/smaps_rollup 2>/dev/null; }
runcfg(){
  local label="$1"; shift
  pkill -f runsc-sandbox 2>/dev/null; sleep 2
  env "$@" $R --root /run/gt --platform=kvm --ignore-cgroups --network=none run -bundle $B gt > $H/gt.log 2>&1 &
  sleep 8
  local out=""; local envseen=""
  for pid in $(pgrep -f "runsc-sandbox.*root=/run/gt"); do out="$(sentry_priv $pid)"; envseen="$(tr "\0" "\n" < /proc/$pid/environ 2>/dev/null | grep -iE "GOGC|GOMEMLIMIT" | tr "\n" " ")"; done
  echo "[$label] sentry: $out   | env-seen: ${envseen:-none}" >> $H/gotune.txt
  $R --root /run/gt --platform=kvm --ignore-cgroups --network=none kill gt KILL 2>/dev/null
  $R --root /run/gt --platform=kvm --ignore-cgroups --network=none delete --force gt 2>/dev/null
}
runcfg "default"          
runcfg "GOGC=10"          GOGC=10
runcfg "GOGC=off+MEMLIMIT" GOGC=off GOMEMLIMIT=20MiB
runcfg "GOGC=20+MEMLIMIT"  GOGC=20 GOMEMLIMIT=24MiB
echo "=== DONE ===" >> $H/gotune.txt
