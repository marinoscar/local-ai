#!/usr/bin/env node
// Echo server for the gateway load test (docs/specs/gateway.md section 9, issue GW-12). TEST ONLY.
//
// It plays the part of a llama-server behind the gateway:
//   GET  /health                  200 {"status":"ok"}
//   GET  /stats                   {"active","started","completed","aborted","rejected"} (what the server saw, for the report)
//   POST /v1/chat/completions     streaming (`"stream": true`): a role chunk, TOKENS content events INTERVAL_MS apart, a
//                                 finish chunk, a usage-only chunk (`"choices": []`) and `data: [DONE]`.
//                                 Not streaming: one JSON completion with `usage`.
//
// PER-EVENT TIMING. Every chunk carries a top-level `"x_sent_ms"`: the server's wall clock, in milliseconds with a fraction
// (`performance.timeOrigin + performance.now()`), taken immediately before the write. The k6 script subtracts it from its
// own clock when the event arrives; the difference is what the path between the two added. The two clocks are the same
// when both run on one machine, which is how the fake scenario runs.
//
// It needs no dependency and no build: `node fake-upstream.mjs --port 18999`.
//   --port <n>          default 18999 (0 = any free port; the chosen port is printed)
//   --host <addr>       default 127.0.0.1
//   --tokens <n>        content events per stream, default 300
//   --interval-ms <n>   milliseconds between two content events, default 20
// The same settings can come from FAKE_UPSTREAM_PORT, FAKE_UPSTREAM_HOST, FAKE_UPSTREAM_TOKENS, FAKE_UPSTREAM_INTERVAL_MS.
import { createServer } from 'node:http';
import { pathToFileURL } from 'node:url';
import { parseArgs } from 'node:util';

export const DEFAULTS = { port: 18999, host: '127.0.0.1', tokens: 300, intervalMs: 20 };

/** Milliseconds since the epoch, with a fraction: comparable with the k6 script's `Date.now()` on the same machine. */
export const nowMs = () => performance.timeOrigin + performance.now();

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, Math.max(0, ms)));

const chunk = (delta, finishReason, extra = {}) =>
  JSON.stringify({
    id: 'chatcmpl-fake',
    object: 'chat.completion.chunk',
    model: 'fake-load-model',
    choices: [{ index: 0, delta, finish_reason: finishReason }],
    x_sent_ms: nowMs(),
    ...extra,
  });

const usageChunk = (promptTokens, completionTokens) =>
  JSON.stringify({
    id: 'chatcmpl-fake',
    object: 'chat.completion.chunk',
    model: 'fake-load-model',
    choices: [],
    usage: { prompt_tokens: promptTokens, completion_tokens: completionTokens, total_tokens: promptTokens + completionTokens },
    x_sent_ms: nowMs(),
  });

/** Builds the server. `listen` it yourself, or use {@link start}. */
export function createFakeUpstream({ tokens = DEFAULTS.tokens, intervalMs = DEFAULTS.intervalMs } = {}) {
  const stats = { active: 0, started: 0, completed: 0, aborted: 0, rejected: 0 };

  async function stream(request, response, promptTokens) {
    stats.started += 1;
    stats.active += 1;
    let closed = false;
    response.on('close', () => {
      closed = true;
    });
    response.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' });
    response.flushHeaders();

    // Backpressure: a write that returns false waits for `drain` (or for the socket to go) before the next one.
    const write = async (payload) => {
      if (closed) return false;
      if (!response.write(`data: ${payload}\n\n`)) {
        await new Promise((resolve) => {
          const done = () => {
            response.off('drain', done);
            response.off('close', done);
            resolve();
          };
          response.once('drain', done);
          response.once('close', done);
        });
      }
      return !closed;
    };

    let ok = await write(chunk({ role: 'assistant', content: '' }, null));
    // Deadlines are absolute (start + i x interval), so a late timer does not push every later event back.
    const startedAt = performance.now();
    for (let i = 0; ok && i < tokens; i += 1) {
      await sleep(startedAt + (i + 1) * intervalMs - performance.now());
      ok = await write(chunk({ content: `t${i} ` }, null));
    }
    ok = ok && (await write(chunk({}, 'stop')));
    ok = ok && (await write(usageChunk(promptTokens, tokens)));
    ok = ok && (await write('[DONE]'));
    stats.active -= 1;
    if (ok) {
      stats.completed += 1;
      response.end();
    } else {
      stats.aborted += 1;
    }
  }

  const server = createServer((request, response) => {
    const url = request.url ?? '/';
    if (request.method === 'GET' && url === '/health') {
      response.writeHead(200, { 'content-type': 'application/json' });
      response.end('{"status":"ok"}');
      return;
    }
    if (request.method === 'GET' && url === '/stats') {
      response.writeHead(200, { 'content-type': 'application/json' });
      response.end(JSON.stringify(stats));
      return;
    }
    if (request.method === 'POST' && url === '/v1/chat/completions') {
      const parts = [];
      request.on('data', (part) => parts.push(part));
      request.on('end', () => {
        // The gateway always sends the node's key; a call without one is the gateway's bug, so say so loudly.
        if (!/^Bearer \S+/.test(request.headers.authorization ?? '')) {
          stats.rejected += 1;
          response.writeHead(401, { 'content-type': 'application/json' });
          response.end(JSON.stringify({ error: { message: 'missing bearer', type: 'authentication_error', code: 401 } }));
          return;
        }
        let body = {};
        try {
          body = JSON.parse(Buffer.concat(parts).toString('utf8'));
        } catch {
          stats.rejected += 1;
          response.writeHead(400, { 'content-type': 'application/json' });
          response.end(JSON.stringify({ error: { message: 'invalid JSON', type: 'invalid_request_error', code: 400 } }));
          return;
        }
        const promptTokens = Array.isArray(body.messages) ? Math.max(1, body.messages.length * 5) : 5;
        if (body.stream === true) {
          void stream(request, response, promptTokens);
          return;
        }
        stats.started += 1;
        stats.completed += 1;
        response.writeHead(200, { 'content-type': 'application/json' });
        response.end(
          JSON.stringify({
            id: 'chatcmpl-fake',
            object: 'chat.completion',
            model: 'fake-load-model',
            choices: [{ index: 0, message: { role: 'assistant', content: 'ok' }, finish_reason: 'stop' }],
            usage: { prompt_tokens: promptTokens, completion_tokens: 1, total_tokens: promptTokens + 1 },
          }),
        );
      });
      return;
    }
    response.writeHead(404, { 'content-type': 'application/json' });
    response.end('{"error":{"message":"not found","type":"invalid_request_error","code":404}}');
  });
  // Plenty of simultaneous sockets; nothing here times a stream out.
  server.requestTimeout = 0;
  server.headersTimeout = 0;
  server.keepAliveTimeout = 30_000;
  return { server, stats };
}

/** Listens and resolves with `{ server, stats, port }`. */
export function start({ port = DEFAULTS.port, host = DEFAULTS.host, ...options } = {}) {
  const fake = createFakeUpstream(options);
  return new Promise((resolve, reject) => {
    fake.server.once('error', reject);
    fake.server.listen(port, host, () => resolve({ ...fake, port: fake.server.address().port }));
  });
}

const number = (value, fallback) => {
  const parsed = Number(value);
  return value !== undefined && value !== '' && Number.isFinite(parsed) && parsed >= 0 ? parsed : fallback;
};

async function main() {
  const { values } = parseArgs({
    options: { port: { type: 'string' }, host: { type: 'string' }, tokens: { type: 'string' }, 'interval-ms': { type: 'string' } },
  });
  const env = process.env;
  const config = {
    port: number(values.port ?? env.FAKE_UPSTREAM_PORT, DEFAULTS.port),
    host: values.host ?? env.FAKE_UPSTREAM_HOST ?? DEFAULTS.host,
    tokens: number(values.tokens ?? env.FAKE_UPSTREAM_TOKENS, DEFAULTS.tokens),
    intervalMs: number(values['interval-ms'] ?? env.FAKE_UPSTREAM_INTERVAL_MS, DEFAULTS.intervalMs),
  };
  const { server, port } = await start(config);
  console.log(`fake-upstream listening on http://${config.host}:${port} (${config.tokens} tokens, ${config.intervalMs} ms apart)`);
  const stop = () => {
    server.close(() => process.exit(0));
    server.closeAllConnections();
  };
  process.on('SIGINT', stop);
  process.on('SIGTERM', stop);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((error) => {
    console.error(`fake-upstream: ${error instanceof Error ? error.message : error}`);
    process.exit(1);
  });
}
