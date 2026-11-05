// What the stream proxy learns from the events it forwards, without copying or fully parsing the stream (docs/specs/
// gateway.md section 4.2, Stream, step 3, and "Usage when missing"):
//   * `data: [DONE]` ends the stream.
//   * The usage-only chunk (`"choices":[]` with a `usage` object, sent because the gateway forced
//     `stream_options.include_usage`) is parsed, its `usage` recorded, and it is dropped unless the CLIENT asked for it.
//   * Every event that carries text counts as a content-bearing event: the estimate of the completion tokens when the
//     node never reported usage (llama-server sends about one token per event).
// Only an event that looks like it carries `usage` is JSON-parsed (one per stream); every other event costs one pass of a
// few regular expressions over a few hundred bytes.
import { parseUsage, type ParsedUsage, type UsageSource } from '../usage/usage-sink';

/** `data: [DONE]`, the last event of every stream. */
const DONE_EVENT = /^data:[ \t]*\[DONE\][ \t]*$/;

/** `"usage": {` : a real usage object. OpenAI-style nodes send `"usage":null` on every chunk, which must not match. */
const USAGE_OBJECT = /"usage"\s*:\s*\{/;

/** `"choices": []` : a chunk with no choices. */
const EMPTY_CHOICES = /"choices"\s*:\s*\[\s*\]/;

/**
 * A non-empty string in `delta.content` (chat), `text` (completions) or `delta.reasoning_content` (thinking models, which
 * llama-server streams in that field). `null` and `""` do not match. A key inside a JSON string value is escaped
 * (`\"content\"`), so text typed by a user cannot match.
 */
const CONTENT_BEARING = /"(?:content|reasoning_content|text)"\s*:\s*"(?!")/;

/** The `data:` field(s) of one event joined as the SSE standard says (a leading space after the colon is dropped). */
export function eventData(text: string): string {
  const lines: string[] = [];
  for (const line of text.split(/\r?\n/)) {
    if (line.startsWith('data:')) lines.push(line.startsWith('data: ') ? line.slice(6) : line.slice(5));
  }
  return lines.join('\n');
}

export interface EventVerdict {
  /** Write this event to the client. False only for the usage-only chunk the client did not ask for. */
  forward: boolean;
  /** `data: [DONE]`: the last event. */
  done: boolean;
}

/** The usage figures of a stream, as `ProxyRequest.settle` takes them. */
export interface StreamUsage {
  promptTokens: number | null;
  completionTokens: number | null;
  usageSource: UsageSource;
}

/** Per-stream state: feed it every complete event in order. */
export class StreamUsageTracker {
  private usage: ParsedUsage | null = null;
  private contentEvents = 0;
  private sawDone = false;

  constructor(private readonly clientWantsUsageChunk: boolean) {}

  /** True once `data: [DONE]` was seen. */
  get finished(): boolean {
    return this.sawDone;
  }

  /** Events that carried text so far. */
  get contentBearingEvents(): number {
    return this.contentEvents;
  }

  inspect(event: Buffer): EventVerdict {
    const text = event.toString('utf8');
    if (DONE_EVENT.test(text.trim())) {
      this.sawDone = true;
      return { forward: true, done: true };
    }

    if (USAGE_OBJECT.test(text)) {
      const parsed = parseUsage(parseJson(eventData(text)));
      if (parsed) {
        this.usage = parsed;
        if (EMPTY_CHOICES.test(text) && !this.clientWantsUsageChunk) return { forward: false, done: false };
      }
    }

    if (CONTENT_BEARING.test(text)) this.contentEvents += 1;
    return { forward: true, done: false };
  }

  /** The node's own figures when it sent them, else the estimate (spec: completion = content-bearing events, prompt unknown). */
  result(): StreamUsage {
    if (this.usage) return { promptTokens: this.usage.promptTokens, completionTokens: this.usage.completionTokens, usageSource: 'upstream' };
    return estimateUsage(this.contentEvents);
  }
}

/** The estimate used when the node reported no usage: abort, mid-stream error, an old node, a completions stream. */
export function estimateUsage(contentBearingEvents: number): StreamUsage {
  return { promptTokens: null, completionTokens: contentBearingEvents, usageSource: 'estimated' };
}

function parseJson(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return null;
  }
}
