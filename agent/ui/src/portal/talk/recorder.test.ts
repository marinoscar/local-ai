import { describe, expect, it } from "vitest";
import { splitInHalf } from "../speech/player";
import { pickMime } from "./recorder";

describe("pickMime", () => {
  it("prefers WebM Opus (Chromium)", () => {
    expect(pickMime(() => true)).toBe("audio/webm;codecs=opus");
  });
  it("falls back to Ogg Opus (Firefox)", () => {
    expect(pickMime((t) => t.startsWith("audio/ogg"))).toBe(
      "audio/ogg;codecs=opus",
    );
  });
  it("lets the browser choose when nothing matches", () => {
    expect(pickMime(() => false)).toBe("");
  });
});

describe("splitInHalf", () => {
  it("cuts at the space nearest the middle", () => {
    expect(splitInHalf("aaaa bbbb cccc dddd")).toEqual(["aaaa bbbb", "cccc dddd"]);
  });
  it("gives up on text with no space", () => {
    expect(splitInHalf("abcdefghij")).toBeNull();
  });
});
