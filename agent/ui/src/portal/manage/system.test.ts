import { afterEach, describe, expect, it, vi } from "vitest";
import { setLang } from "../i18n/i18n";
import {
  EXPERT_COMMAND,
  desktopHost,
  doneText,
  failureError,
  isRestartable,
  kitSummary,
  parseResult,
  refreshText,
  serviceName,
  stateKey,
  stateLevel,
} from "./system";

// Issue #500 (dashboard.md 4.6): the System screen's rules without a browser. setLang writes
// document.documentElement.lang, so a stand-in document is enough here.
vi.stubGlobal("document", { documentElement: { lang: "en" } });

afterEach(() => setLang("en"));

describe("services", () => {
  it("only llama, kiwix and whisper have a Restart button", () => {
    expect(["llama", "kiwix", "whisper", "piper", "maps", "x"].map(isRestartable)).toEqual([true, true, true, false, false, false]);
  });

  it("names each service in plain words, in both languages", () => {
    expect(serviceName("kiwix")).toBe("Library");
    expect(serviceName("llama")).toBe("AI brain");
    expect(serviceName("future")).toBe("future");
    setLang("es");
    expect(serviceName("kiwix")).toBe("Biblioteca");
  });

  it("uses the health state, else what systemd says", () => {
    expect(stateKey({ name: "kiwix", unit: "u", state: "missing_content" })).toBe("system.state.missing_content");
    expect(stateKey({ name: "llama", unit: "u", active: "active", healthy: true })).toBe("system.state.ok");
    expect(stateKey({ name: "llama", unit: "u", active: "active", healthy: false })).toBe("system.state.degraded");
    expect(stateKey({ name: "whisper", unit: "u", active: "failed" })).toBe("system.state.down");
    expect(stateKey({ name: "piper", unit: null })).toBe("system.state.unknown");
    expect(stateKey({ name: "piper", unit: null, state: "weird" })).toBe("system.state.unknown");
    expect(stateLevel("system.state.ok")).toBe("green");
    expect(stateLevel("system.state.down")).toBe("red");
    expect(stateLevel("system.state.starting")).toBe("amber");
  });
});

describe("kitSummary", () => {
  it("counts the kit items of the plan and the verified ones", () => {
    const row = (id: string, kind: string, verified: boolean, in_profile = true) => ({ id, kind, verified, in_profile });
    expect(
      kitSummary([row("a", "kit", true), row("b", "kit", false), row("c", "kiwix", true), row("d", "kit", false, false)]),
    ).toEqual({ total: 2, ready: 1 });
    expect(kitSummary([])).toEqual({ total: 0, ready: 0 });
  });
});

describe("desktopHost", () => {
  it("is null in a plain browser and for anything that is not the bridge", () => {
    expect(desktopHost({})).toBeNull();
    expect(desktopHost(undefined)).toBeNull();
    expect(desktopHost({ localaiDesktopHost: { isDesktop: "yes" } })).toBeNull();
  });

  it("has no runner for a desktop older than #500, and calls the member on the bridge", async () => {
    expect(desktopHost({ localaiDesktopHost: { isDesktop: true, version: "1.0.0" } })).toEqual({ version: "1.0.0", run: null });
    const runSystemAction = vi.fn(async () => ({ ok: true }));
    const host = desktopHost({ localaiDesktopHost: { isDesktop: true, version: 3, runSystemAction } });
    expect(host?.version).toBe("");
    await host?.run?.("kit-verify");
    expect(runSystemAction).toHaveBeenCalledWith("kit-verify");
  });
});

describe("answers of runSystemAction", () => {
  it("keeps only known fields and types", () => {
    expect(parseResult(null)).toEqual({ ok: false, reason: "failed", code: undefined, message: undefined, fix: undefined });
    expect(parseResult({ ok: true, overall: "purple", files: "3", to: "0.2.0" })).toMatchObject({ ok: true, overall: undefined, files: undefined, to: "0.2.0" });
    expect(parseResult({ ok: false, reason: "cancelled" })).toMatchObject({ ok: false, reason: "cancelled" });
  });

  it("a failure shows the command's code, message and fix, else the reason's sentence", () => {
    const e = failureError({ ok: false, reason: "failed", code: "LAI-ARK-001", message: "No AI-ARK drive", fix: "Connect it." });
    expect([e.code, e.message, e.fix]).toEqual(["LAI-ARK-001", "No AI-ARK drive", "Connect it."]);
    expect(failureError({ ok: false, reason: "cancelled" }).message).toMatch(/password window was closed/);
    expect(failureError({ ok: false, reason: "not_implemented" }).message).toMatch(/cannot show the password window/);
    expect(failureError({ ok: false, reason: "something-new" }).message).toMatch(/stopped with an error/);
  });

  it("words a success from its fields", () => {
    expect(doneText("update-agent", { ok: true, status: "updated", from: "0.1.0", to: "0.2.0" })).toBe("Updated to version 0.2.0. Local AI restarted.");
    expect(doneText("update-agent", { ok: true, status: "already_current", from: "0.1.0", to: "0.1.0" })).toBe("You already have the newest version (0.1.0).");
    expect(doneText("kit-verify", { ok: true, overall: "green" })).toBe("The rebuild kit is complete and undamaged.");
    expect(doneText("kit-verify", { ok: true, overall: "red", code: "LAI-KIT-001" })).toMatch(/missing or damaged.*Code: LAI-KIT-001$/);
    expect(doneText("backup-config", { ok: true, files: 14 })).toBe("Saved on the AI-ARK drive (14 files). Keep the drive in a safe place.");
    expect(doneText("restore-config", { ok: true, restored: 0 })).toMatch(/^Nothing to restore/);
    expect(doneText("restore-config", { ok: true, restored: 3, restarted: true })).toBe("Restored 3 files. Local AI restarted.");
    setLang("es");
    expect(doneText("backup-config", { ok: true })).toBe("Se guardó en el disco AI-ARK. Guarde el disco en un lugar seguro.");
  });

  it("words the catalog refresh", () => {
    expect(refreshText({ version: "2026.11.14-1", previous: "2026.11.02-1", changed: true })).toBe("The catalog was updated to 2026.11.14-1.");
    expect(refreshText({ version: "2026.11.02-1", previous: "2026.11.02-1", changed: false })).toBe("You already have the newest catalog (2026.11.02-1).");
  });
});

describe("the commands for an expert", () => {
  it("are the documented aictl commands (aictl.md 4.27, 4.23c, 4.25, 4.19)", () => {
    expect(EXPERT_COMMAND).toEqual({
      "update-agent": "sudo aictl update",
      "kit-verify": "aictl kit verify",
      "backup-config": "sudo aictl backup-config --to-ark",
      "restore-config": "sudo aictl backup-config --restore latest",
      "pin-set": "sudo aictl pin set",
    });
  });
});
