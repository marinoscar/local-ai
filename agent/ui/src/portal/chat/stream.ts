import { apiFetch } from "../api";

// The streaming chat client of the Talk and Photo flows: POST /chat/v1/chat/completions with
// stream:true, read as OpenAI server-sent events (survival-portal.md sections 4.5 and 4.6).

export type ContentPart =
  | { type: "text"; text: string }
  | { type: "image_url"; image_url: { url: string } };

export interface ChatMessage {
  role: "system" | "user" | "assistant";
  content: string | ContentPart[];
}

export interface ChatBodyOptions {
  model: string;
  system: string;
  /** Earlier turns, oldest first. */
  history?: ChatMessage[];
  /** The new user message. */
  user: ChatMessage["content"];
  /** The model reasons: ask it not to, so spoken answers start fast. */
  reasoning?: boolean;
  maxTokens?: number;
  temperature?: number;
}

/** buildChatBody is the request body of survival-portal.md section 4.5 step 4. */
export function buildChatBody(o: ChatBodyOptions): Record<string, unknown> {
  const body: Record<string, unknown> = {
    model: o.model,
    stream: true,
    temperature: o.temperature ?? 0.5,
    max_tokens: o.maxTokens ?? 600,
    messages: [
      { role: "system", content: o.system },
      ...(o.history ?? []),
      { role: "user", content: o.user },
    ],
  };
  if (o.reasoning) body.chat_template_kwargs = { enable_thinking: false };
  return body;
}

/**
 * SseParser turns text chunks, cut anywhere, into the data payloads of complete events. Lines end in
 * LF, CR or CRLF; an event ends at an empty line; comment lines (":") are ignored.
 */
export class SseParser {
  private buf = "";
  private data: string[] = [];

  push(chunk: string): string[] {
    this.buf += chunk;
    const out: string[] = [];
    for (;;) {
      const m = /\r\n|\n|\r/.exec(this.buf);
      if (!m) break;
      // A lone CR at the very end may be the first half of CRLF: wait for more.
      if (m[0] === "\r" && m.index + 1 === this.buf.length) break;
      const line = this.buf.slice(0, m.index);
      this.buf = this.buf.slice(m.index + m[0].length);
      if (line === "") {
        if (this.data.length > 0) out.push(this.data.join("\n"));
        this.data = [];
      } else if (line.startsWith("data:")) {
        const v = line.slice(5);
        this.data.push(v.startsWith(" ") ? v.slice(1) : v);
      }
    }
    return out;
  }
}

/** The end marker of the stream. */
export const DONE = "[DONE]";

/**
 * deltaText is the answer text of one event payload. reasoning_content (a model thinking aloud) is
 * ignored on purpose; [DONE] and anything that is not a chat chunk give "".
 */
export function deltaText(payload: string): string {
  if (payload === DONE) return "";
  try {
    const j = JSON.parse(payload) as {
      choices?: { delta?: { content?: unknown } }[];
    };
    const c = j.choices?.[0]?.delta?.content;
    return typeof c === "string" ? c : "";
  } catch {
    return "";
  }
}

export interface StreamOptions {
  body: Record<string, unknown>;
  signal: AbortSignal;
  /** Called with each piece of answer text as it arrives. */
  onText: (piece: string) => void;
}

/**
 * streamChat posts the request and calls onText for every text delta until the stream ends. It
 * resolves with the whole answer. A non-2xx answer is an ApiError (from apiFetch); an abort rejects
 * with the AbortError.
 */
export async function streamChat(o: StreamOptions): Promise<string> {
  const res = await apiFetch("/chat/v1/chat/completions", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Accept: "text/event-stream",
    },
    body: JSON.stringify(o.body),
    signal: o.signal,
  });
  if (!res.body) throw new Error("no response body");
  const reader = res.body.getReader();
  const dec = new TextDecoder();
  const parser = new SseParser();
  let all = "";
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    for (const ev of parser.push(dec.decode(value, { stream: true }))) {
      const piece = deltaText(ev);
      if (piece) {
        all += piece;
        o.onText(piece);
      }
    }
  }
  for (const ev of parser.push(dec.decode() + "\n\n")) {
    const piece = deltaText(ev);
    if (piece) {
      all += piece;
      o.onText(piece);
    }
  }
  return all;
}
