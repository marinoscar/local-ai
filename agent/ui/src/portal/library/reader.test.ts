import { describe, expect, it } from "vitest";
import { parseReaderSearch, readerPath, safeReaderSrc } from "./reader";

describe("safeReaderSrc", () => {
  it.each([
    "/library/content/b/A",
    "/maps/",
    "/docs/runbooks/01.html",
    // Issue #431: the chat opens in the reader so ⌂ Home is always on screen.
    "/chat/",
  ])("allows %s", (src) => {
    expect(safeReaderSrc(src)).toBe(src);
  });

  it.each([
    "/chatter/",
    "/chat",
    "//evil.example/chat/",
    "https://evil.example/chat/",
    "/chat/../api/v1/status",
    "/chat/%2e%2e/api",
    "/chat\\x",
    "/portal",
    "",
  ])("refuses %s", (src) => {
    expect(safeReaderSrc(src)).toBeNull();
  });
});

describe("readerPath", () => {
  it("round-trips the chat into the reader with Home as the way back", () => {
    const path = readerPath("/chat/", "/portal");
    expect(path).toBe("/portal/read?src=%2Fchat%2F&back=%2Fportal");
    expect(parseReaderSearch(path.slice(path.indexOf("?")))).toEqual({ src: "/chat/", back: "/portal" });
  });
});
