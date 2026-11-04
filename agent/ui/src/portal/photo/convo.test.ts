import { describe, expect, it } from "vitest";
import { canAsk, MAX_TURNS, photoBody } from "./convo";

const IMG = "data:image/jpeg;base64,AAAA";
const base = { model: "vlm-1", system: "SYS", imageUrl: IMG };

interface Msg {
  role: string;
  content: unknown;
}

describe("photoBody", () => {
  it("sends the image with the first question in the vision format", () => {
    const b = photoBody({ ...base, turns: [], question: "What is this?" });
    expect(b).toMatchObject({
      model: "vlm-1",
      stream: true,
      temperature: 0.2,
      max_tokens: 1024,
    });
    expect(b.messages).toEqual([
      { role: "system", content: "SYS" },
      {
        role: "user",
        content: [
          { type: "text", text: "What is this?" },
          { type: "image_url", image_url: { url: IMG } },
        ],
      },
    ]);
  });

  it("keeps the image in the first user message only", () => {
    const b = photoBody({
      ...base,
      turns: [
        { question: "What is this?", answer: "A plant." },
        { question: "Safe?", answer: "Unsure." },
      ],
      question: "How do I wash it off?",
    });
    const msgs = b.messages as Msg[];
    expect(msgs.map((m) => m.role)).toEqual([
      "system",
      "user",
      "assistant",
      "user",
      "assistant",
      "user",
    ]);
    expect(msgs.filter((m) => JSON.stringify(m).includes("image_url"))).toHaveLength(1);
    expect(JSON.stringify(msgs[1])).toContain("image_url");
    expect(msgs[3]).toEqual({ role: "user", content: "Safe?" });
    expect(msgs[5]).toEqual({ role: "user", content: "How do I wash it off?" });
  });

  it("is not a reasoning request", () => {
    const b = photoBody({ ...base, turns: [], question: "q" });
    expect(b).not.toHaveProperty("chat_template_kwargs");
  });
});

describe("canAsk", () => {
  it("allows four questions", () => {
    const t = { question: "q", answer: "a" };
    expect(MAX_TURNS).toBe(4);
    expect(canAsk([])).toBe(true);
    expect(canAsk([t, t, t])).toBe(true);
    expect(canAsk([t, t, t, t])).toBe(false);
  });
});
