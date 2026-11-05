/*
 * Gateway load test (docs/specs/gateway.md section 9, issue GW-12). RUN BY HAND, NEVER IN CI.
 *
 * WHAT IT MEASURES
 *   SCENARIO=fake  (default) 200 concurrent streams for 5 minutes against the gateway running with the fake-upstream flag
 *                  (an echo server sends 300 tokens, 20 ms apart, each event stamped with its send time). The gateway must
 *                  add <= 5 ms p95 per event, the API's RSS must stay under 600 MB, there must be 0 errors and every stream
 *                  must complete.
 *   SCENARIO=lab   8 concurrent streams PER NODE for 10 minutes against the real lab (owner, off-hours, a service key).
 *                  Pass: no 5xx from the gateway itself, per-node inflight matches llama --parallel (look at `aictl status`
 *                  on each node while it runs), Open WebUI still usable.
 *
 * INSTALL k6 (an external binary; it is not an npm dependency)
 *   This script needs k6 1.2 or newer AND the xk6-sse extension (k6/x/sse), because plain k6 buffers a response and cannot
 *   time the events of a stream. With k6 1.2+ and a normal internet connection, `k6 run` builds a k6 with the extension the
 *   first time (automatic extension resolution: it prints "provisioning"); later runs reuse it.
 *     Debian/Ubuntu  https://grafana.com/docs/k6/latest/set-up/install-k6/  (apt repository or the release tarball)
 *     macOS          brew install k6
 *     Check          k6 version        (v1.2.0 or newer)
 *   Offline machine: build once with `xk6 build --with github.com/grafana/xk6-sse` and put that k6 on the PATH.
 *   Set K6_BINARY_PROVISIONING=false to forbid the automatic build.
 *
 * RUN THE FAKE SCENARIO (three terminals, from the repository root; docs/runbooks/gateway-load-test.md has the full steps)
 *   1. node apps/cloud/apps/api/test/load/fake-upstream.mjs --port 18999
 *   2. LOCALAI_GATEWAY_FAKE_UPSTREAM=1 npm run start:dev --workspace=api         (the API refuses this in production)
 *   3. LAI_KEY="$(cat ~/.config/opencode/localai-gateway-key)" SCENARIO=fake \
 *        BASE_URL=http://127.0.0.1:3000/api/gateway/v1 k6 run apps/cloud/apps/api/test/load/gateway-load.k6.js
 *      (or: LAI_KEY=... npm run load:gateway --workspace=api)
 *   The key is a service key created on the API keys page; it must allow the model `fake-load-model` and have limits of at
 *   least 200 concurrent and 600 requests per minute (a service key made with gateway:admin may be given them).
 *   Memory: in a fourth terminal, `apps/cloud/apps/api/test/load/sample-rss.sh "$(pgrep -n -f 'dist/main')" rss.csv`.
 *   Baseline (what the echo server and k6 add by themselves): run the same command with
 *   BASE_URL=http://127.0.0.1:18999/v1 (straight at the echo server, no gateway) and subtract.
 *
 * RUN THE LAB SCENARIO (owner, off-hours; a real key, real models, real GPUs)
 *   LAI_KEY="$(cat ~/.config/opencode/localai-gateway-key)" SCENARIO=lab MODEL=<a loaded model id> NODES=3 \
 *     k6 run apps/cloud/apps/api/test/load/gateway-load.k6.js
 *   BASE_URL defaults to https://ai.marin.cr/v1. VUs = NODES x PER_NODE (8). While it runs, watch `aictl status` on each node.
 *
 * SETTINGS (environment)
 *   LAI_KEY        required, never written in this file or in any output
 *   SCENARIO       fake | lab                              (fake)
 *   BASE_URL       gateway base, ending in /v1             (fake: http://127.0.0.1:3000/api/gateway/v1; lab: https://ai.marin.cr/v1)
 *   MODEL          model id                                (fake: fake-load-model; lab: required)
 *   fake only:     VUS=200  RAMP_S=60  HOLD_S=300  RAMPDOWN_S=30  TOKENS=300  INTERVAL_MS=20  (the last two must match the echo server)
 *   lab only:      NODES=1  PER_NODE=8  DURATION=10m  MAX_TOKENS=256  PROMPT=<text>
 *   SUMMARY_JSON   path: also write the numbers below as JSON
 *
 * RESULT. The end-of-test summary prints the fields of RESULTS-TEMPLATE.md: p50/p95/p99 added latency, errors, streams
 * completed. RSS comes from sample-rss.sh. The clocks of the echo server and of k6 are compared directly, so both must run
 * on one machine; Date.now() has 1 ms resolution, so a single event is accurate to about +-1 ms.
 */
import sse from 'k6/x/sse';
import { sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

const env = (name, fallback) => (__ENV[name] !== undefined && __ENV[name] !== '' ? __ENV[name] : fallback);
const int = (name, fallback) => {
  const value = Number.parseInt(env(name, String(fallback)), 10);
  if (!Number.isFinite(value) || value < 0) throw new Error(`${name} must be a non-negative integer`);
  return value;
};

const SCENARIO = env('SCENARIO', 'fake');
if (SCENARIO !== 'fake' && SCENARIO !== 'lab') throw new Error('SCENARIO must be fake or lab');
const LAI_KEY = env('LAI_KEY', '');
if (LAI_KEY === '') throw new Error('LAI_KEY is required (a gateway service key); pass it in the environment, never in a file');

const BASE_URL = env('BASE_URL', SCENARIO === 'fake' ? 'http://127.0.0.1:3000/api/gateway/v1' : 'https://ai.marin.cr/v1').replace(/\/+$/, '');
const MODEL = env('MODEL', SCENARIO === 'fake' ? 'fake-load-model' : '');
if (MODEL === '') throw new Error('MODEL is required for SCENARIO=lab (a model id that is loaded on the nodes)');

// ---- the numbers of the scenarios ------------------------------------------------------------------------------------
const TOKENS = int('TOKENS', 300);
const INTERVAL_MS = int('INTERVAL_MS', 20);
const VUS = int('VUS', 200);
const RAMP_S = int('RAMP_S', 60);
const HOLD_S = int('HOLD_S', 300);
const RAMPDOWN_S = int('RAMPDOWN_S', 30);
const NODES = int('NODES', 1);
const PER_NODE = int('PER_NODE', 8);
const DURATION = env('DURATION', '10m');
const MAX_TOKENS = int('MAX_TOKENS', 256);
const PROMPT = env('PROMPT', 'Write a short story about a lighthouse keeper who finds a message in a bottle.');

// A stream takes TOKENS x INTERVAL_MS plus the setup; during the hold every VU restarts at once, so about
// VUS x HOLD_S / stream seconds streams finish. The floor leaves 10% for ramp edges and scheduling.
const STREAM_SECONDS = (TOKENS * INTERVAL_MS) / 1000;
const MIN_STREAMS_FAKE = Math.floor((0.9 * VUS * HOLD_S) / (STREAM_SECONDS + 0.5));

// ---- metrics ----------------------------------------------------------------------------------------------------------
const addedMs = new Trend('gateway_added_ms', true); // event receive time - echo server send time (fake scenario)
const ttfbMs = new Trend('stream_first_event_ms', true); // request start -> first content event
const streamsStarted = new Counter('streams_started');
const streamsCompleted = new Counter('streams_completed');
const streamsIncomplete = new Counter('streams_incomplete'); // started and did not finish cleanly
const streamFailed = new Rate('stream_failed');
const gateway5xx = new Counter('gateway_5xx'); // HTTP status >= 500 on the stream request
const errorEvents = new Counter('stream_error_events'); // an `error` object inside the stream (the gateway's mid-stream error event)

export const options = {
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
  scenarios:
    SCENARIO === 'fake'
      ? {
          fake: {
            executor: 'ramping-vus',
            exec: 'fakeStream',
            startVUs: 0,
            stages: [
              { duration: `${RAMP_S}s`, target: VUS },
              { duration: `${HOLD_S}s`, target: VUS },
              { duration: `${RAMPDOWN_S}s`, target: 0 },
            ],
            // A stream in progress at the end of the ramp-down is allowed to finish (it must not count as incomplete).
            gracefulRampDown: `${Math.ceil(STREAM_SECONDS) + 30}s`,
            gracefulStop: `${Math.ceil(STREAM_SECONDS) + 30}s`,
          },
        }
      : {
          lab: {
            executor: 'constant-vus',
            exec: 'labStream',
            vus: NODES * PER_NODE,
            duration: DURATION,
            gracefulStop: '120s',
          },
        },
  thresholds:
    SCENARIO === 'fake'
      ? {
          gateway_added_ms: ['p(95)<=5'],
          http_req_failed: ['rate==0'],
          stream_failed: ['rate==0'],
          streams_incomplete: ['count==0'],
          gateway_5xx: ['count==0'],
          stream_error_events: ['count==0'],
          // The issue asks for `count==<expected>`; a ramping executor has no exact count, so the floor is a minimum and
          // `streams_incomplete == 0` is what proves that every stream that started also completed.
          streams_completed: [`count>=${MIN_STREAMS_FAKE}`],
        }
      : {
          gateway_5xx: ['count==0'],
          stream_error_events: ['count==0'],
        },
};

// ---- one stream -------------------------------------------------------------------------------------------------------
let logged = 0;
function note(message) {
  if (logged < 5) {
    logged += 1;
    console.warn(message);
  }
}

/**
 * Opens one streaming chat completion and reads it to the end.
 * `expectedContent`: content events that must arrive (null = any number); `measure`: compute the added latency.
 */
function runStream(body, expectedContent, measure) {
  streamsStarted.add(1);
  const startedAt = Date.now();
  let content = 0;
  let sawDone = false;
  let sawUsage = false;
  let errored = false;
  let first = true;

  const response = sse.open(
    `${BASE_URL}/chat/completions`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream', Authorization: `Bearer ${LAI_KEY}` },
      body: JSON.stringify(body),
      tags: { scenario: SCENARIO },
    },
    (client) => {
      client.on('event', (event) => {
        const receivedAt = Date.now();
        const data = event.data;
        if (data === '[DONE]') {
          sawDone = true;
          return;
        }
        let parsed;
        try {
          parsed = JSON.parse(data);
        } catch (_) {
          errored = true;
          note('a stream event was not JSON');
          return;
        }
        if (parsed.error) {
          errorEvents.add(1);
          errored = true;
          note(`stream error event: ${String(parsed.error.localai_code || parsed.error.code || 'unknown')}`);
          return;
        }
        if (parsed.usage && Array.isArray(parsed.choices) && parsed.choices.length === 0) sawUsage = true;
        const delta = parsed.choices && parsed.choices[0] && parsed.choices[0].delta;
        if (delta && typeof delta.content === 'string' && delta.content !== '') {
          content += 1;
          if (first) {
            first = false;
            ttfbMs.add(receivedAt - startedAt);
          }
        }
        // The echo server stamps every chunk; Date.now() is whole milliseconds, so the stamp is floored to match.
        if (measure && typeof parsed.x_sent_ms === 'number') addedMs.add(receivedAt - Math.floor(parsed.x_sent_ms));
      });
      client.on('error', (e) => {
        errored = true;
        note(`stream connection error: ${e && typeof e.error === 'function' ? e.error() : 'unknown'}`);
      });
    },
  );

  const status = response ? response.status : 0;
  if (status >= 500) gateway5xx.add(1);
  else gateway5xx.add(0);
  const contentOk = expectedContent === null ? content > 0 : content === expectedContent;
  const complete = status === 200 && sawDone && !errored && contentOk && (expectedContent === null || sawUsage);
  streamFailed.add(!complete);
  streamsCompleted.add(complete ? 1 : 0);
  streamsIncomplete.add(complete ? 0 : 1);
  if (!complete) {
    note(`stream incomplete: status=${status} content=${content}/${expectedContent === null ? 'any' : expectedContent} done=${sawDone} usage=${sawUsage}`);
    sleep(1); // a failing target must not be hammered in a tight loop
  }
}

export function fakeStream() {
  // include_usage: the usage-only chunk is forwarded only to a client that asks for it, and a complete stream ends with it.
  runStream({ model: MODEL, stream: true, stream_options: { include_usage: true }, messages: [{ role: 'user', content: 'Say something long.' }] }, TOKENS, true);
}

export function labStream() {
  runStream(
    { model: MODEL, stream: true, max_tokens: MAX_TOKENS, stream_options: { include_usage: true }, messages: [{ role: 'user', content: PROMPT }] },
    null,
    false,
  );
}

// ---- the summary: the fields of RESULTS-TEMPLATE.md -----------------------------------------------------------------
const metricValues = (data, name) => (data.metrics[name] && data.metrics[name].values) || {};
const count = (data, name) => metricValues(data, name).count || 0;
const fixed = (value, digits) => (typeof value === 'number' ? value.toFixed(digits) : 'n/a');

function thresholdLines(data) {
  const lines = [];
  for (const name of Object.keys(data.metrics)) {
    const thresholds = data.metrics[name].thresholds;
    if (!thresholds) continue;
    for (const expression of Object.keys(thresholds)) {
      const entry = thresholds[expression];
      const ok = typeof entry === 'object' && entry !== null ? entry.ok === true : entry === false; // older k6: true = failed
      lines.push(`  ${ok ? 'PASS' : 'FAIL'}  ${name}: ${expression}`);
    }
  }
  return lines;
}

export function handleSummary(data) {
  const added = metricValues(data, 'gateway_added_ms');
  const first = metricValues(data, 'stream_first_event_ms');
  const started = count(data, 'streams_started');
  const completed = count(data, 'streams_completed');
  const result = {
    scenario: SCENARIO,
    base_url_host: BASE_URL.replace(/^(https?:\/\/[^/]+).*$/, '$1'),
    model: MODEL,
    streams_started: started,
    streams_completed: completed,
    streams_incomplete: count(data, 'streams_incomplete'),
    errors: {
      failed_stream_rate: metricValues(data, 'stream_failed').rate || 0,
      gateway_5xx: count(data, 'gateway_5xx'),
      stream_error_events: count(data, 'stream_error_events'),
    },
    gateway_added_ms: { p50: added.med, p90: added['p(90)'], p95: added['p(95)'], p99: added['p(99)'], avg: added.avg, max: added.max },
    first_event_ms: { p50: first.med, p95: first['p(95)'] },
  };
  const text = [
    '',
    `Gateway load test, scenario ${SCENARIO} (${result.base_url_host}, model ${MODEL})`,
    SCENARIO === 'fake'
      ? `  added latency per event (ms)   p50 ${fixed(added.med, 2)}   p95 ${fixed(added['p(95)'], 2)}   p99 ${fixed(added['p(99)'], 2)}   max ${fixed(added.max, 2)}   (target: p95 <= 5)`
      : `  first event (ms)               p50 ${fixed(first.med, 0)}   p95 ${fixed(first['p(95)'], 0)}`,
    `  streams started / completed    ${started} / ${completed}   incomplete ${result.streams_incomplete}`,
    `  errors                         failed-stream rate ${fixed(result.errors.failed_stream_rate * 100, 2)}%   5xx ${result.errors.gateway_5xx}   error events ${result.errors.stream_error_events}`,
    '  API RSS max (MiB)              from sample-rss.sh (target: < 600), fill in RESULTS-TEMPLATE.md',
    '',
    ...thresholdLines(data),
    '',
  ].join('\n');
  const outputs = { stdout: text };
  const file = env('SUMMARY_JSON', '');
  if (file !== '') outputs[file] = JSON.stringify(result, null, 2);
  return outputs;
}
