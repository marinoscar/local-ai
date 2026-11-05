# Gateway load test results

Copy this file, fill it in, and paste it into the pull request (or the issue) that records the run.
Targets: [gateway.md section 9](../../../../../../docs/specs/gateway.md#load-testing). Never write a key, an address or a person's name here.

## Run

| Field | Value |
|---|---|
| Date (UTC) | |
| Scenario | `fake` / `lab` |
| Machine (CPU model, cores, RAM, OS) | |
| Everything on one machine? (k6, echo server and API; required for `fake`) | yes / no |
| API started with | `npm run start:dev` / `node dist/main.js` |
| Node.js | |
| k6 (`k6 version`, with xk6-sse) | |
| Commit of the repository | |
| Settings that differ from the defaults (`VUS`, `HOLD_S`, `TOKENS`, `INTERVAL_MS`, `NODES`, ...) | none |

## Result (copy from the k6 summary and from `sample-rss.sh`)

| Measure | Target | Result | Pass |
|---|---|---|---|
| Added latency per event, p50 (ms) | n/a | | |
| Added latency per event, p95 (ms) | <= 5 | | |
| Added latency per event, p99 (ms) | n/a | | |
| Errors (failed streams, 5xx, error events) | 0 | | |
| Streams started / completed | all complete | / | |
| API RSS max (MiB), from the CSV | < 600 | | |
| Peak concurrent streams reached (k6 `vus_max`) | 200 | | |

Baseline, k6 straight at the echo server (no gateway), same settings:

| Measure | Result |
|---|---|
| Added latency per event, p50 / p95 / p99 (ms) | / / |

## Real lab scenario only (`SCENARIO=lab`)

| Check | Result |
|---|---|
| Streams per node held for 10 minutes | |
| 5xx from the gateway | 0 |
| In-flight count per node (`aictl status`) equals that node's `--parallel` | yes / no, per node: |
| Open WebUI still usable during the run | yes / no |

## Notes

What was odd, what was retried, any bug filed (link the issue; a missed target is a bug against streaming, #142).
