#!/usr/bin/env bash
# Samples the resident set size (RSS) of one process into a CSV, every 5 seconds, until the process exits.
# Used by the gateway load test (docs/specs/gateway.md section 9, issue GW-12): the API's RSS must stay under 600 MB.
#
#   sample-rss.sh <pid> [out.csv]
#
#   <pid>      the API's node process. With `npm run start:dev` that is the node running dist/main, not the npm or nest
#              wrapper:  sample-rss.sh "$(pgrep -n -f 'dist/main')" rss.csv
#   out.csv    default rss-<pid>.csv in the current directory (overwritten)
#   SAMPLE_INTERVAL_S   seconds between samples (default 5)
#
# The CSV has the header `ts,rss_mib` and one row per sample: `ts` is UTC ISO-8601, `rss_mib` is MiB with one decimal.
# When the process exits the script prints the maximum on stderr and exits 0. Stop it early with Ctrl-C (same summary).
# Works on Linux and macOS (it uses `ps`, not /proc). Exit codes: 0 done, 2 bad usage or no such process.
set -u

pid="${1:-}"
case "$pid" in
  '' | *[!0-9]*)
    echo "usage: $0 <pid> [out.csv]" >&2
    exit 2
    ;;
esac
out="${2:-rss-$pid.csv}"
interval="${SAMPLE_INTERVAL_S:-5}"
case "$interval" in
  '' | *[!0-9]* | 0)
    echo "SAMPLE_INTERVAL_S must be a positive whole number" >&2
    exit 2
    ;;
esac

if ! kill -0 "$pid" 2>/dev/null; then
  echo "no process with pid $pid" >&2
  exit 2
fi

echo 'ts,rss_mib' >"$out"
max=0
samples=0

summary() {
  echo "sample-rss: pid $pid, $samples samples, max RSS ${max} MiB, written to $out" >&2
}
trap 'summary; exit 0' INT TERM

while kill -0 "$pid" 2>/dev/null; do
  kib="$(ps -o rss= -p "$pid" 2>/dev/null | tr -d ' ')"
  if [ -n "$kib" ]; then
    mib="$(awk -v k="$kib" 'BEGIN { printf "%.1f", k / 1024 }')"
    printf '%s,%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$mib" >>"$out"
    samples=$((samples + 1))
    max="$(awk -v a="$max" -v b="$mib" 'BEGIN { print (b + 0 > a + 0) ? b : a }')"
  fi
  sleep "$interval" &
  wait $! 2>/dev/null
done
summary
