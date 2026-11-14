import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

// The CSP that the fake agent and the offline test use is the one in the spec (survival-portal.md
// section 4.11); the Go test checks the code's constant against the same line.
const root = resolve(__dirname, "../../../..");
const spec = readFileSync(
  resolve(root, "docs/specs/survival-portal.md"),
  "utf8",
);
const m = spec.match(/`Content-Security-Policy: ([^`]+)`/);

describe("Portal CSP", () => {
  it("is in the spec", () => {
    expect(m).not.toBeNull();
    expect(m![1]).not.toContain("unsafe-inline");
  });
  for (const f of [
    "agent/ui/e2e/fake-agent.mjs",
    "agent/ui/e2e/offline.spec.ts",
  ]) {
    it(`is the same in ${f}`, () => {
      expect(readFileSync(resolve(root, f), "utf8")).toContain(m![1]);
    });
  }
});

// A merge once dropped a "}" from manage.css: every rule after it, the fonts included, was lost.
describe("stylesheets", () => {
  it("every Portal stylesheet has balanced braces", async () => {
    const { readdirSync, readFileSync } = await import("node:fs");
    const { join } = await import("node:path");
    const dir = join(__dirname, "styles");
    for (const f of readdirSync(dir).filter((n) => n.endsWith(".css"))) {
      const css = readFileSync(join(dir, f), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
      let depth = 0;
      for (const ch of css) {
        if (ch === "{") depth++;
        if (ch === "}") depth--;
        expect(depth, f).toBeGreaterThanOrEqual(0);
        expect(depth, f).toBeLessThanOrEqual(2);
      }
      expect(depth, f).toBe(0);
    }
  });
});
