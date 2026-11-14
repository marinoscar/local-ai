import { describe, expect, it, vi } from "vitest";
import type { MusicStatus } from "@localai/ui/music";
import { setLang } from "../i18n/i18n";
import { screenOf } from "../router";
import type { PortalStatus } from "../status";
import en from "./i18n/en.json";
import es from "./i18n/es.json";
import { mt } from "./i18n/i18n";
import { musicView, showLibrary, showStudio } from "./view";

const portal = (over: Partial<PortalStatus> = {}) =>
  ({ mode: "normal", session: "full", ...over }) as PortalStatus;

const music = (over: Partial<MusicStatus> = {}): MusicStatus => ({
  installed: true,
  loaded_model: "song-a",
  engine_state: "ready",
  queue: { running: 0, queued: 0, max: 4 },
  ...over,
});

const st = (status: MusicStatus | null, error = null) => ({ status, error });

describe("musicView (state first)", () => {
  it("waits for the answers", () => {
    expect(musicView(null, st(null)).kind).toBe("loading");
    expect(musicView(portal(), st(null)).kind).toBe("loading");
  });

  it("explains survival mode and a missing sign-in before asking the agent", () => {
    expect(musicView(portal({ mode: "survival" }), st(music())).kind).toBe(
      "survival",
    );
    expect(musicView(portal({ session: "portal" }), st(music())).kind).toBe(
      "sign_in",
    );
    expect(musicView(portal({ session: "none" }), st(null)).kind).toBe(
      "sign_in",
    );
  });

  it("walks the engine states", () => {
    expect(musicView(portal(), st(music({ installed: false }))).kind).toBe(
      "not_installed",
    );
    expect(
      musicView(portal(), st(music({ engine_state: "stopped", loaded_model: null })))
        .kind,
    ).toBe("not_loaded");
    expect(
      musicView(portal(), st(music({ engine_state: "ready", loaded_model: null })))
        .kind,
    ).toBe("not_loaded");
    expect(musicView(portal(), st(music({ engine_state: "starting" }))).kind).toBe(
      "starting",
    );
    expect(musicView(portal(), st(music({ engine_state: "stopping" }))).kind).toBe(
      "stopping",
    );
    expect(musicView(portal(), st(music({ engine_state: "failed" }))).kind).toBe(
      "failed",
    );
    expect(musicView(portal(), st(music())).kind).toBe("ready");
  });

  it("shows a failed status read with its code, but keeps a good last answer", () => {
    const error = { code: "LAI-SVC-116", message: "", fix: "" };
    expect(musicView(portal(), { status: null, error })).toEqual({
      kind: "error",
      ...error,
    });
    expect(musicView(portal(), { status: music(), error }).kind).toBe("ready");
  });

  it("shows the songs whenever they can be read, the studio only when music is coming or ready", () => {
    expect(showLibrary({ kind: "not_loaded" })).toBe(true);
    expect(showLibrary({ kind: "ready" })).toBe(true);
    expect(showLibrary({ kind: "not_installed" })).toBe(false);
    expect(showLibrary({ kind: "sign_in" })).toBe(false);
    expect(showStudio({ kind: "ready" })).toBe(true);
    expect(showStudio({ kind: "starting" })).toBe(true);
    expect(showStudio({ kind: "not_loaded" })).toBe(false);
  });
});

describe("the /music/ address", () => {
  it("opens the music screen from /music/, /music and /portal/music", () => {
    expect(screenOf("/music/")).toBe("music");
    expect(screenOf("/music")).toBe("music");
    expect(screenOf("/portal/music")).toBe("music");
    expect(screenOf("/musical")).toBe("");
  });
});

describe("music words", () => {
  it("have the same keys and placeholders in English and Spanish, none empty", () => {
    expect(Object.keys(es).sort()).toEqual(Object.keys(en).sort());
    const ph = (s: string) => (s.match(/\{\w+\}/g) ?? []).sort();
    for (const k of Object.keys(en) as (keyof typeof en)[]) {
      expect(ph(es[k]), k).toEqual(ph(en[k]));
      expect(es[k].trim(), k).not.toBe("");
    }
  });

  it("follow the Portal language", () => {
    vi.stubGlobal("document", { documentElement: {} });
    setLang("es");
    expect(mt("music.notLoaded")).toBe("La música no está cargada.");
    setLang("en");
    expect(mt("music.notLoaded")).toBe("Music is not loaded.");
    vi.unstubAllGlobals();
  });

  it("never tell a person to type a command", () => {
    for (const v of [...Object.values(en), ...Object.values(es)])
      expect(v).not.toMatch(/aictl|sudo|systemctl/);
  });
});
