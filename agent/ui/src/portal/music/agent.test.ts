import { describe, expect, it } from "vitest";
import { TransportError, type MusicOp } from "@localai/ui/music";
import { ApiError } from "../api";
import {
  agentMusic,
  agentWorkloads,
  eventFields,
  generateForm,
  pickMusicModel,
  SWITCH_TIMEOUT_MS,
  type AgentDeps,
  type EventLike,
} from "./agent";

type Handler = (e: EventLike) => void;

class FakeES {
  handlers = new Map<string, Handler[]>();
  closed = false;
  constructor(readonly types: string) {}
  addEventListener(type: string, fn: Handler) {
    this.handlers.set(type, [...(this.handlers.get(type) ?? []), fn]);
  }
  close() {
    this.closed = true;
  }
  /** Sends an event as the agent does: the envelope with op_id, the fields in data. */
  send(type: string, data: Record<string, unknown>, opId?: string) {
    const body = JSON.stringify({ id: 1, type, op_id: opId, data });
    for (const h of this.handlers.get(type) ?? []) h({ data: body });
  }
}

type Route = (init?: RequestInit) => Response | Error;

function harness(routes: Record<string, Route>) {
  const calls: { path: string; init?: RequestInit }[] = [];
  const streams: FakeES[] = [];
  const intervals: (() => void)[] = [];
  const timeouts: (() => void)[] = [];
  const deps: AgentDeps = {
    fetch: async (path, init) => {
      calls.push({ path, init });
      const key = `${init?.method ?? "GET"} ${path}`;
      const r = routes[key];
      if (!r) throw new ApiError(404, "LAI-AGT-021", `no route ${key}`);
      const out = r(init);
      if (out instanceof Error) throw out;
      return out;
    },
    openEvents: (types) => {
      const es = new FakeES(types);
      streams.push(es);
      return es;
    },
    setInterval: (fn) => {
      intervals.push(fn);
      return intervals.length - 1;
    },
    clearInterval: (h) => {
      intervals[h as number] = () => undefined;
    },
    setTimeout: (fn) => {
      timeouts.push(fn);
      return timeouts.length - 1;
    },
    clearTimeout: (h) => {
      timeouts[h as number] = () => undefined;
    },
  };
  const tick = async () => {
    for (const f of [...intervals]) f();
    await flush();
  };
  return { deps, calls, streams, tick, timeouts };
}

const ok = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

const flush = () => new Promise((r) => setTimeout(r, 0));

const WL = {
  workloads: [
    {
      name: "coding",
      label: { en: "Coding", es: "Programación" },
      models: ["c"],
      active: true,
      fits: true,
      missing: [],
    },
    {
      name: "music",
      label: { en: "Music", es: "Música" },
      models: ["m"],
      active: false,
      fits: true,
      missing: [],
    },
  ],
  active: "coding",
};

describe("eventFields", () => {
  it("reads the fields from data and keeps the envelope's op_id", () => {
    expect(
      eventFields({ data: '{"op_id":"op1","data":{"state":"running"}}' }),
    ).toEqual({ state: "running", op_id: "op1" });
    expect(eventFields({ data: '{"data":{"op_id":"x"}}' })).toEqual({
      op_id: "x",
    });
    expect(eventFields({ data: "not json" })).toEqual({});
  });
});

describe("agentWorkloads", () => {
  it("activates through the workloads route and follows the events of its operation", async () => {
    const h = harness({
      "GET /api/v1/workloads": () => ok(WL),
      "POST /api/v1/workloads/music/activate": () => ok({ op_id: "op1" }, 202),
    });
    const tr = agentWorkloads(h.deps, { musicFallback: true });
    expect((await tr.list()).active).toBe("coding");
    const progress: (number | null)[] = [];
    let done = false;
    const p = tr
      .activate("music", { onProgress: (x) => progress.push(x.progress) })
      .then(() => (done = true));
    await flush();
    const post = h.calls.find((c) => c.init?.method === "POST");
    expect(JSON.parse(String(post?.init?.body))).toEqual({
      wait: false,
      force: false,
    });
    const es = h.streams[0];
    expect(es.types).toBe("workload.*");
    es.send("workload.progress", { progress: 0.3, step: "loading" }, "op1");
    es.send("workload.progress", { progress: 0.9 }, "other-op");
    expect(progress).toEqual([0.3]);
    es.send("workload.done", { state: "done" }, "op1");
    await p;
    expect(done).toBe(true);
    expect(es.closed).toBe(true);
  });

  it("rejects with the code of a failed activation (LAI-LLM-052 by default)", async () => {
    const h = harness({
      "GET /api/v1/workloads": () => ok(WL),
      "POST /api/v1/workloads/music/activate": () => ok({ op_id: "op1" }, 202),
    });
    const tr = agentWorkloads(h.deps);
    const p = tr.activate("music");
    await flush();
    h.streams[0].send("workload.done", { state: "failed" }, "op1");
    await expect(p).rejects.toMatchObject({ code: "LAI-LLM-052" });

    const p2 = tr.activate("music");
    await flush();
    h.streams[1].send(
      "workload.done",
      { error: { code: "LAI-LLM-051", message: "Too big", fix: "Use a bigger card." } },
      "op1",
    );
    await expect(p2).rejects.toMatchObject({
      code: "LAI-LLM-051",
      message: "Too big",
      fix: "Use a bigger card.",
    });
  });

  it("passes on a refusal of the route with its code and fix", async () => {
    const h = harness({
      "POST /api/v1/workloads/music/activate": () =>
        new ApiError(409, "LAI-LLM-053", "Survival mode.", "Leave survival first."),
    });
    const err = await agentWorkloads(h.deps)
      .activate("music", { force: true })
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(TransportError);
    expect(err).toMatchObject({
      code: "LAI-LLM-053",
      fix: "Leave survival first.",
      status: 409,
    });
    expect(
      JSON.parse(String(h.calls[0].init?.body)).force,
    ).toBe(true);
  });

  it("finishes by polling when the event was missed", async () => {
    const wl = structuredClone(WL);
    const h = harness({
      "GET /api/v1/workloads": () => ok(wl),
      "POST /api/v1/workloads/music/activate": () => ok({ op_id: "op1" }, 202),
    });
    const p = agentWorkloads(h.deps).activate("music");
    await flush();
    await h.tick();
    wl.active = "music";
    await h.tick();
    await expect(p).resolves.toBeUndefined();
  });

  it("gives up after the switch timeout with LAI-LLM-041", async () => {
    const h = harness({
      "POST /api/v1/workloads/music/activate": () => ok({ op_id: "op1" }, 202),
    });
    const p = agentWorkloads(h.deps).activate("music");
    await flush();
    expect(SWITCH_TIMEOUT_MS).toBeGreaterThan(60_000);
    h.timeouts[0]();
    await expect(p).rejects.toMatchObject({ code: "LAI-LLM-041" });
  });

  it("makes up a Music workload from the first acestep model when the profile has none", async () => {
    const h = harness({
      "GET /api/v1/workloads": () =>
        ok({ workloads: [WL.workloads[0]], active: "coding" }),
      "GET /api/v1/models": () =>
        ok({
          models: [
            { id: "chat-a", engine: "llama", loadable: true },
            { id: "song-b", engine: "acestep", loadable: false, present: true },
            { id: "song-a", engine: "acestep", loadable: true, present: true },
          ],
        }),
      "POST /api/v1/models/song-a/load": () => ok({}, 202),
      "GET /api/v1/music/status": () =>
        ok({ installed: true, loaded_model: "song-a", engine_state: "ready", queue: { running: 0, queued: 0, max: 4 } }),
    });
    const tr = agentWorkloads(h.deps, { musicFallback: true });
    const list = await tr.list();
    expect(list.workloads.map((w) => w.name)).toEqual(["coding", "music"]);
    expect(list.workloads[1]).toMatchObject({
      label: { en: "Music", es: "Música" },
      models: ["song-a"],
      active: false,
    });
    const p = tr.activate("music");
    await flush();
    expect(h.calls.some((c) => c.path === "/api/v1/models/song-a/load")).toBe(true);
    expect(h.streams[0].types).toBe("model.*");
    h.streams[0].send("model.loaded", { model: "song-a", load_ms: 10 });
    await expect(p).resolves.toBeUndefined();
  });

  it("still offers the fallback on an agent without the workloads routes", async () => {
    const h = harness({
      "GET /api/v1/models": () =>
        ok({ models: [{ id: "song-a", engine: "acestep", loaded: true }] }),
    });
    const list = await agentWorkloads(h.deps, { musicFallback: true }).list();
    expect(list.active).toBe("music");
    await expect(agentWorkloads(h.deps).list()).rejects.toMatchObject({
      status: 404,
    });
  });

  it("reports a failed model load with its code", async () => {
    const h = harness({
      "GET /api/v1/workloads": () => ok({ workloads: [], active: null }),
      "GET /api/v1/models": () =>
        ok({ models: [{ id: "song-a", engine: "acestep", loadable: true }] }),
      "POST /api/v1/models/song-a/load": () => ok({}, 202),
    });
    const tr = agentWorkloads(h.deps, { musicFallback: true });
    await tr.list();
    const p = tr.activate("music");
    await flush();
    h.streams[0].send("model.failed", { model: "song-a", code: "LAI-LLM-043", message: "no runtime" });
    await expect(p).rejects.toMatchObject({ code: "LAI-LLM-043" });
  });
});

describe("pickMusicModel", () => {
  it("prefers a loaded, then a loadable, then a present model", () => {
    expect(pickMusicModel([])).toBeNull();
    expect(
      pickMusicModel([
        { id: "a", engine: "acestep" },
        { id: "b", engine: "acestep", present: true },
        { id: "c", engine: "acestep", loadable: true },
      ])?.id,
    ).toBe("c");
    expect(
      pickMusicModel([
        { id: "c", engine: "acestep", loadable: true },
        { id: "d", roles: ["music"], loaded: true },
      ])?.id,
    ).toBe("d");
  });
});

describe("agentMusic", () => {
  it("sends the generate form as multipart, leaving out what is not set", async () => {
    const f = generateForm({
      lyrics: "[verse]\nhi",
      tags: "calm",
      duration_s: 30,
      language: "es",
      seed: null,
      variations: 2,
      sample: null,
      sample_mode: null,
      strength: null,
    });
    expect([...f.keys()].sort()).toEqual(
      ["duration_s", "language", "lyrics", "tags", "variations"].sort(),
    );
    const s = new File(["x"], "s.wav", { type: "audio/wav" });
    const g = generateForm({
      lyrics: "",
      tags: "t",
      duration_s: 60,
      language: "en",
      seed: 7,
      variations: 1,
      sample: s,
      sample_mode: "same_song",
      strength: 1,
    });
    expect(g.get("seed")).toBe("7");
    expect(g.get("sample_mode")).toBe("same_song");
    expect(g.get("strength")).toBe("1");
    expect((g.get("sample") as File).name).toBe("s.wav");
  });

  it("posts the song and answers the operation", async () => {
    const h = harness({
      "POST /api/v1/music/generate": () =>
        ok({ op_id: "op9", song_ids: ["s1"] }, 202),
    });
    const tr = agentMusic(h.deps);
    const acc = await tr.generate({
      lyrics: "x",
      tags: "",
      duration_s: 60,
      language: "en",
      seed: null,
      variations: 1,
      sample: null,
      sample_mode: null,
      strength: null,
    });
    expect(acc).toEqual({ op_id: "op9", song_ids: ["s1"] });
    expect(h.calls[0].init?.body).toBeInstanceOf(FormData);
  });

  it("keeps the code of a refused song", async () => {
    const h = harness({
      "POST /api/v1/music/generate": () =>
        new ApiError(409, "LAI-SVC-151", "Music is not loaded.", "Switch to Music."),
    });
    await expect(
      agentMusic(h.deps).generate({
        lyrics: "x",
        tags: "",
        duration_s: 60,
        language: "en",
        seed: null,
        variations: 1,
        sample: null,
        sample_mode: null,
        strength: null,
      }),
    ).rejects.toMatchObject({ code: "LAI-SVC-151", fix: "Switch to Music." });
  });

  it("follows an operation by events and polling, and stops at its end", async () => {
    let op: Record<string, unknown> = { state: "queued", progress: null, song_ids: [] };
    const h = harness({
      "GET /api/v1/music/ops/op1": () => ok(op),
    });
    const tr = agentMusic(h.deps);
    const seen: MusicOp[] = [];
    tr.watch("op1", (o) => seen.push(o));
    await flush();
    expect(seen.at(-1)?.state).toBe("queued");
    const es = h.streams[0];
    expect(es.types).toBe("music.*");
    es.send("music.progress", { op_id: "op1", state: "running", progress: 0.5 });
    es.send("music.progress", { op_id: "op2", state: "running", progress: 0.9 });
    expect(seen.at(-1)).toMatchObject({ state: "running", progress: 0.5 });
    op = { state: "failed", progress: 0.6, song_ids: [], error: { code: "LAI-SVC-152", message: "crash" } };
    es.send("music.done", { op_id: "op1", state: "failed" });
    await flush();
    expect(seen.at(-1)).toMatchObject({
      state: "failed",
      error: { code: "LAI-SVC-152", message: "crash" },
    });
    expect(es.closed).toBe(true);
    const n = seen.length;
    await h.tick();
    expect(seen.length).toBe(n);
  });

  it("lists, plays, cancels and deletes songs on the contract's paths", async () => {
    const h = harness({
      "GET /api/v1/music/songs?limit=20&cursor=c1": () =>
        ok({ songs: [{ id: "a b" }], next_cursor: null }),
      "POST /api/v1/music/ops/op1/cancel": () => ok({}),
      "DELETE /api/v1/music/songs/a%20b": () => ok({}),
    });
    const tr = agentMusic(h.deps);
    const page = await tr.listSongs({ limit: 20, cursor: "c1" });
    expect(page.songs).toHaveLength(1);
    expect(tr.audioUrl(page.songs[0])).toBe("/api/v1/music/songs/a%20b/audio");
    await tr.cancel("op1");
    await tr.deleteSong("a b");
    const del = h.calls.at(-1);
    expect(del?.init?.method).toBe("DELETE");
    expect(new Headers(del?.init?.headers).has("X-LocalAI-Pin-Grant")).toBe(false);
  });

  it("turns the admin PIN into a grant before deleting", async () => {
    const h = harness({
      "POST /api/v1/pin/verify": () =>
        ok({ grant: "pg_test", expires_at: "2026-11-14T00:00:00Z" }),
      "DELETE /api/v1/music/songs/s1": () => ok({}),
    });
    await agentMusic(h.deps).deleteSong("s1", { pin: "1234" });
    expect(JSON.parse(String(h.calls[0].init?.body))).toEqual({ pin: "1234" });
    expect(new Headers(h.calls[1].init?.headers).get("X-LocalAI-Pin-Grant")).toBe("pg_test");
  });

  it("reports a wrong PIN with its code and deletes nothing", async () => {
    const h = harness({
      "POST /api/v1/pin/verify": () => new ApiError(401, "LAI-SEC-003", "Wrong PIN"),
    });
    await expect(
      agentMusic(h.deps).deleteSong("s1", { pin: "0000" }),
    ).rejects.toMatchObject({ code: "LAI-SEC-003" });
    expect(h.calls.some((c) => c.init?.method === "DELETE")).toBe(false);
  });

  it("turns a fetch that got no answer into LAI-SVC-116", async () => {
    const h = harness({
      "GET /api/v1/music/status": () => new TypeError("Failed to fetch"),
    });
    await expect(agentMusic(h.deps).status()).rejects.toMatchObject({
      code: "LAI-SVC-116",
    });
  });
});
