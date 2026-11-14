import { describe, expect, it } from "vitest";
import { changeMode, isMode, leaveDefault } from "./mode";

describe("mode helpers (dashboard.md section 4.2)", () => {
  it("survival uses POST /mode/survival, the others POST /mode {target}, never a PIN grant", async () => {
    const calls: unknown[][] = [];
    const send = (async (...a: unknown[]) => {
      calls.push(a);
      return null;
    }) as Parameters<typeof changeMode>[1];
    await changeMode("survival", send);
    await changeMode("online", send);
    await changeMode("offline", send);
    expect(calls).toEqual([
      ["POST", "/mode/survival", {}],
      ["POST", "/mode", { target: "online" }],
      ["POST", "/mode", { target: "offline" }],
    ]);
  });
  it("Leave survival preselects Online only when the internet was seen", () => {
    expect(leaveDefault("full")).toBe("online");
    for (const c of ["none", "limited", "not_checked", undefined]) expect(leaveDefault(c)).toBe("offline");
  });
  it("knows the three modes", () => {
    expect(["online", "offline", "survival"].every(isMode)).toBe(true);
    expect(isMode("dashboard")).toBe(false);
  });
});
