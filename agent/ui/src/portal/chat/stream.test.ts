import { describe, expect, it } from "vitest";
import { buildChatBody, deltaText, SseParser } from "./stream";

const chunk = (s: string) =>
  `data: ${JSON.stringify({ choices: [{ delta: { content: s } }] })}\n\n`;

describe("SseParser", () => {
  it("parses whole events", () => {
    const p = new SseParser();
    expect(p.push(chunk("Hi") + chunk(" there") + "data: [DONE]\n\n")).toEqual([
      '{"choices":[{"delta":{"content":"Hi"}}]}',
      '{"choices":[{"delta":{"content":" there"}}]}',
      "[DONE]",
    ]);
  });

  it("handles events split at any byte", () => {
    const text = chunk("Hola") + chunk(" mundo") + "data: [DONE]\n\n";
    const whole = new SseParser().push(text);
    for (let cut = 1; cut < text.length; cut++) {
      const p = new SseParser();
      const got = [...p.push(text.slice(0, cut)), ...p.push(text.slice(cut))];
      expect(got, `cut at ${cut}`).toEqual(whole);
    }
  });

  it("handles CRLF, comments and multi-line data", () => {
    const p = new SseParser();
    expect(p.push(": keep-alive\r\n\r\ndata: a\r\ndata: b\r\n\r\n")).toEqual([
      "a\nb",
    ]);
  });

  it("waits for the blank line", () => {
    const p = new SseParser();
    expect(p.push("data: x\n")).toEqual([]);
    expect(p.push("\n")).toEqual(["x"]);
  });
});

describe("deltaText", () => {
  it("reads content", () => {
    expect(deltaText('{"choices":[{"delta":{"content":"ok"}}]}')).toBe("ok");
  });
  it("ignores reasoning_content, [DONE], empty and broken payloads", () => {
    expect(
      deltaText('{"choices":[{"delta":{"reasoning_content":"hmm"}}]}'),
    ).toBe("");
    expect(deltaText("[DONE]")).toBe("");
    expect(deltaText('{"choices":[]}')).toBe("");
    expect(deltaText("{not json")).toBe("");
  });
});

describe("buildChatBody", () => {
  const base = {
    model: "m1",
    system: "SYS",
    user: "hello",
    history: [{ role: "assistant" as const, content: "earlier" }],
  };
  it("has the spec'd shape", () => {
    expect(buildChatBody(base)).toEqual({
      model: "m1",
      stream: true,
      temperature: 0.5,
      max_tokens: 600,
      messages: [
        { role: "system", content: "SYS" },
        { role: "assistant", content: "earlier" },
        { role: "user", content: "hello" },
      ],
    });
  });
  it("adds chat_template_kwargs only for a reasoning model", () => {
    expect(buildChatBody(base)).not.toHaveProperty("chat_template_kwargs");
    expect(buildChatBody({ ...base, reasoning: false })).not.toHaveProperty(
      "chat_template_kwargs",
    );
    expect(buildChatBody({ ...base, reasoning: true })).toMatchObject({
      chat_template_kwargs: { enable_thinking: false },
    });
  });
});
