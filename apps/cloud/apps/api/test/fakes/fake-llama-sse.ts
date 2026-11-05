// Streaming behaviours for the fake llama-server (test/fakes/fake-llama.ts): a scripted SSE answer with real timing, so
// the stream proxy is exercised against a node that sends a token every N milliseconds, stalls, dies, or writes faster
// than a client reads. Every emission is timestamped (`performance.now()`, the same clock the client side uses) so a spec
// can prove that no event was held back.
import type { ServerResponse } from 'node:http';

import type { FakeLlamaBehaviour, FakeLlamaRequest } from './fake-llama';

export const sleep = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));

export interface SseScript {
  tokens: string[];
  /** Milliseconds between two events. */
  intervalMs?: number;
  /** After this many token events: `destroy` the socket, `end` the body without `[DONE]`, or `stall` (go quiet, keep the socket open). */
  after?: { tokens: number; action: 'destroy' | 'end' | 'stall' };
  /** End every event with `\r\n\r\n` instead of `\n\n`. */
  crlf?: boolean;
  /** Usage reported in the usage-only chunk when the request asked for `include_usage` (default 11 / token count). */
  promptTokens?: number;
  /** Keep emitting `repeat` tokens forever (a stream that never ends by itself). */
  endless?: boolean;
  /** Filled by the behaviour: when each event was written (`performance.now()`), in order. */
  emittedAt?: number[];
  /** Filled by the behaviour: the raw text of each event, in order. */
  emitted?: string[];
}

export function chatChunk(content: string | null, extra: Record<string, unknown> = {}): string {
  return JSON.stringify({ id: 'chatcmpl-1', object: 'chat.completion.chunk', choices: [{ index: 0, delta: content === null ? { role: 'assistant', content: null } : { content }, finish_reason: null }], ...extra });
}

export function textChunk(text: string): string {
  return JSON.stringify({ id: 'cmpl-1', object: 'text_completion', choices: [{ index: 0, text, finish_reason: null }] });
}

export const finishChunk = JSON.stringify({ id: 'chatcmpl-1', object: 'chat.completion.chunk', choices: [{ index: 0, delta: {}, finish_reason: 'stop' }] });

export function usageChunk(promptTokens: number, completionTokens: number): string {
  return JSON.stringify({ id: 'chatcmpl-1', object: 'chat.completion.chunk', choices: [], usage: { prompt_tokens: promptTokens, completion_tokens: completionTokens, total_tokens: promptTokens + completionTokens } });
}

/** Writes one event and records when. Resolves when the write was accepted (or the socket drained). */
export async function emit(response: ServerResponse, script: SseScript, payload: string): Promise<void> {
  const terminator = script.crlf ? '\r\n\r\n' : '\n\n';
  const text = `data: ${payload}${terminator}`;
  (script.emittedAt ??= []).push(performance.now());
  (script.emitted ??= []).push(text);
  if (!response.write(text)) await drained(response);
}

/** Resolves on `drain` or when the response closes; leaves no listener behind. */
export function drained(response: ServerResponse): Promise<void> {
  return new Promise<void>((resolve) => {
    const done = (): void => {
      response.off('drain', done);
      response.off('close', done);
      resolve();
    };
    response.once('drain', done);
    response.once('close', done);
  });
}

export function sseHeaders(response: ServerResponse): void {
  response.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' });
  response.flushHeaders();
}

/** The scripted answer. Chat gets `delta.content`, completions get `text`; the usage chunk only when the request asked for it. */
export function sseBehaviour(script: SseScript): FakeLlamaBehaviour {
  return async (request: FakeLlamaRequest, response: ServerResponse) => {
    const chat = request.url !== '/v1/completions';
    const wantsUsage = (JSON.parse(request.body) as { stream_options?: { include_usage?: boolean } }).stream_options?.include_usage === true;
    const interval = script.intervalMs ?? 0;
    sseHeaders(response);
    if (chat) await emit(response, script, chatChunk(null));

    const stop = async (): Promise<boolean> => {
      if (!script.after) return false;
      if (script.after.action === 'destroy') {
        await sleep(30); // let the last event leave the socket: a destroy straight after a write can drop it
        response.destroy();
      }
      else if (script.after.action === 'end') response.end();
      else await new Promise<void>(() => undefined); // stall: never resolves, the socket stays open
      return true;
    };

    for (let index = 0; script.endless || index < script.tokens.length; index += 1) {
      if (script.after && index === script.after.tokens && (await stop())) return;
      if (interval > 0) await sleep(interval);
      if (response.destroyed || response.writableEnded) return;
      const token = script.tokens[index % script.tokens.length]!;
      await emit(response, script, chat ? chatChunk(token) : textChunk(token));
    }
    if (script.after && script.after.tokens >= script.tokens.length && (await stop())) return;
    if (chat) await emit(response, script, finishChunk);
    if (chat && wantsUsage) await emit(response, script, usageChunk(script.promptTokens ?? 11, script.tokens.length));
    await emit(response, script, '[DONE]');
    response.end();
  };
}
