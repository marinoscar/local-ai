import {
  TransportError,
  type GenerateAccepted,
  type GenerateRequest,
  type MusicOp,
  type MusicStatus,
  type MusicTransport,
  type Song,
  type SongsPage,
} from "@localai/ui/music";
import type {
  ActivateOptions,
  Workload,
  WorkloadsResponse,
  WorkloadTransport,
} from "@localai/ui/workloads";
import { ApiError, apiFetch, CODE_LOST_CONTACT } from "../api";

// The agent side of the shared music and workload components (E11 contract section 3): the same-origin REST routes
// under /api/v1 and the SSE stream (/api/v1/events). Every failure becomes a TransportError with a doctor code: the
// agent's own envelope when it sent one, LAI-SVC-116 (lost contact) when it did not answer at all.

const API = "/api/v1";

/** How long a switch may take before the screen says so (LAI-LLM-041). A real switch takes seconds to minutes. */
export const SWITCH_TIMEOUT_MS = 10 * 60_000;
/** The polling safety net under the events (a missed or filtered event never leaves the screen stuck). */
export const SWITCH_POLL_MS = 3000;
export const OP_POLL_MS = 2000;

export const CODE_ENGINE_START_FAILED = "LAI-LLM-040";
export const CODE_ENGINE_NOT_READY = "LAI-LLM-041";
export const CODE_WORKLOAD_FAILED = "LAI-LLM-052";
export const CODE_SONG_FAILED = "LAI-SVC-152";

/** The name and label of the music workload (contract section 1: catalog role `music`, engine `acestep`). */
export const MUSIC_WORKLOAD = "music";
export const MUSIC_LABEL = { en: "Music", es: "Música" };
export const MUSIC_ENGINE = "acestep";

export interface EventLike {
  data: string;
}
export interface EventSourceLike {
  addEventListener(type: string, fn: (e: EventLike) => void): void;
  close(): void;
}

export interface AgentDeps {
  /** apiFetch: same origin, UI language header; throws ApiError on a non-2xx answer. */
  fetch: (path: string, init?: RequestInit) => Promise<Response>;
  /** Opens the event stream for the given type patterns; null when the browser has no EventSource. */
  openEvents: (types: string) => EventSourceLike | null;
  setInterval: (fn: () => void, ms: number) => unknown;
  clearInterval: (h: unknown) => void;
  setTimeout: (fn: () => void, ms: number) => unknown;
  clearTimeout: (h: unknown) => void;
}

export function realDeps(): AgentDeps {
  return {
    fetch: apiFetch,
    openEvents: (types) =>
      typeof EventSource === "undefined"
        ? null
        : new EventSource(`${API}/events?types=${types}`),
    setInterval: (fn, ms) => window.setInterval(fn, ms),
    clearInterval: (h) => window.clearInterval(h as number),
    setTimeout: (fn, ms) => window.setTimeout(fn, ms),
    clearTimeout: (h) => window.clearTimeout(h as number),
  };
}

/** toTransportError keeps the agent's code, message and fix; a fetch that never got an answer is LAI-SVC-116. */
export function toTransportError(e: unknown, fallback: string): TransportError {
  if (e instanceof TransportError) return e;
  if (e instanceof ApiError)
    return new TransportError({
      code: e.code || fallback,
      message: e.message,
      fix: e.fix,
      status: e.status,
    });
  if (e instanceof TypeError)
    return new TransportError({ code: CODE_LOST_CONTACT, message: "" });
  return new TransportError({
    code: fallback,
    message: e instanceof Error ? e.message : "",
  });
}

/** The fields of an event: the stream sends the envelope, whose `data` holds them; `op_id` may sit on either level. */
export function eventFields(e: EventLike): Record<string, unknown> {
  try {
    const body = JSON.parse(e.data) as Record<string, unknown>;
    const inner =
      body.data && typeof body.data === "object"
        ? (body.data as Record<string, unknown>)
        : body;
    return inner.op_id === undefined && body.op_id !== undefined
      ? { ...inner, op_id: body.op_id }
      : inner;
  } catch {
    return {};
  }
}

function num(v: unknown): number | null {
  return typeof v === "number" && Number.isFinite(v) ? v : null;
}

function str(v: unknown): string | null {
  return typeof v === "string" && v !== "" ? v : null;
}

function errorOf(
  v: unknown,
): { code: string; message: string; fix?: string } | null {
  if (!v || typeof v !== "object") return null;
  const o = v as Record<string, unknown>;
  const code = str(o.code);
  if (!code) return null;
  return {
    code,
    message: str(o.message) ?? "",
    fix: str(o.fix) ?? undefined,
  };
}

async function json<T>(
  deps: AgentDeps,
  path: string,
  init?: RequestInit,
  fallback = CODE_LOST_CONTACT,
): Promise<T> {
  try {
    const res = await deps.fetch(path, init);
    const text = await res.text();
    return (text === "" ? {} : JSON.parse(text)) as T;
  } catch (e) {
    throw toTransportError(e, fallback);
  }
}

const POST_JSON = (body: unknown): RequestInit => ({
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify(body),
});

/** The model fields this file reads from GET /models (agent.md section 3.8.7). */
interface ModelRow {
  id: string;
  engine?: string;
  roles?: string[];
  present?: boolean;
  loadable?: boolean;
  loadable_reason?: { code?: string } | null;
  loaded?: boolean;
  status?: string;
}

/** The music model to load when the node has no `music` workload: the first acestep model, a loadable one first. */
export function pickMusicModel(models: ModelRow[]): ModelRow | null {
  const music = models.filter(
    (m) => m.engine === MUSIC_ENGINE || m.roles?.includes("music"),
  );
  return (
    music.find((m) => m.loaded) ??
    music.find((m) => m.loadable) ??
    music.find((m) => m.present) ??
    music[0] ??
    null
  );
}

/** A `music` workload made up from one model, for a node whose profile has none yet (contract section 4). */
export function syntheticMusicWorkload(m: ModelRow): Workload {
  return {
    name: MUSIC_WORKLOAD,
    label: MUSIC_LABEL,
    models: [m.id],
    active: m.loaded === true || m.status === "loaded",
    fits: m.loadable_reason?.code !== "LAI-CAT-005",
    missing: m.present === false ? [m.id] : [],
  };
}

/**
 * waitFor runs `start`, then settles when `check` (polled) or an event says so. It is the shared shape of a workload
 * switch, a model load and a song: listen first (an event faster than the POST answer is not lost), poll as the
 * safety net, stop everything on the first end.
 */
function follow(
  deps: AgentDeps,
  opts: {
    types: string;
    events: Record<string, (f: Record<string, unknown>) => void>;
    poll: () => void;
    pollMs: number;
    timeoutMs?: number;
    onTimeout?: () => void;
  },
): () => void {
  const es = deps.openEvents(opts.types);
  for (const [type, fn] of Object.entries(opts.events))
    es?.addEventListener(type, (e) => fn(eventFields(e)));
  const iv = deps.setInterval(opts.poll, opts.pollMs);
  const to =
    opts.timeoutMs !== undefined && opts.onTimeout
      ? deps.setTimeout(opts.onTimeout, opts.timeoutMs)
      : null;
  return () => {
    es?.close();
    deps.clearInterval(iv);
    if (to !== null) deps.clearTimeout(to);
  };
}

/**
 * agentWorkloads is the WorkloadTransport of the agent. With `musicFallback`, a node whose profile has no `music`
 * workload yet still gets a "Music" button when it has an acestep model: the button loads that model through
 * POST /models/{id}/load (the contract's fallback until a shipped profile carries the workload).
 */
export function agentWorkloads(
  deps: AgentDeps = realDeps(),
  opts: { musicFallback?: boolean } = {},
): WorkloadTransport {
  /** The model behind a made-up workload, by workload name. */
  const synthetic = new Map<string, string>();

  const list = async (): Promise<WorkloadsResponse> => {
    let resp: WorkloadsResponse = { workloads: [], active: null };
    try {
      const r = await json<Partial<WorkloadsResponse>>(
        deps,
        `${API}/workloads`,
      );
      resp = { workloads: r.workloads ?? [], active: r.active ?? null };
    } catch (e) {
      // An agent without the workloads routes (older build) still offers the music fallback.
      if (!(opts.musicFallback && (e as TransportError).status === 404))
        throw e;
    }
    synthetic.clear();
    if (
      opts.musicFallback &&
      !resp.workloads.some((w) => w.name === MUSIC_WORKLOAD)
    ) {
      const r = await json<{ models?: ModelRow[] }>(deps, `${API}/models`);
      const m = pickMusicModel(r.models ?? []);
      if (m) {
        const w = syntheticMusicWorkload(m);
        synthetic.set(w.name, m.id);
        resp = {
          workloads: [...resp.workloads, w],
          active: resp.active ?? (w.active ? w.name : null),
        };
      }
    }
    return resp;
  };

  const activateWorkload = (name: string, o: ActivateOptions) =>
    new Promise<void>((resolve, reject) => {
      let opId: string | null = null;
      let done = false;
      const finish = (err?: TransportError) => {
        if (done) return;
        done = true;
        stop();
        if (err) reject(err);
        else resolve();
      };
      const mine = (f: Record<string, unknown>) =>
        opId === null || f.op_id === undefined || f.op_id === opId;
      const stop = follow(deps, {
        types: "workload.*",
        events: {
          "workload.progress": (f) => {
            if (mine(f))
              o.onProgress?.({
                progress: num(f.progress),
                step: str(f.step),
                model: str(f.model),
              });
          },
          "workload.done": (f) => {
            if (!mine(f)) return;
            const err = errorOf(f.error);
            const failed =
              err !== null || f.state === "failed" || f.ok === false;
            finish(
              failed
                ? new TransportError({
                    code: err?.code ?? CODE_WORKLOAD_FAILED,
                    message: err?.message ?? "",
                    fix: err?.fix,
                  })
                : undefined,
            );
          },
        },
        poll: () => {
          void list().then(
            (r) => {
              if (r.active === name) finish();
            },
            () => undefined,
          );
        },
        pollMs: SWITCH_POLL_MS,
        timeoutMs: SWITCH_TIMEOUT_MS,
        onTimeout: () =>
          finish(
            new TransportError({ code: CODE_ENGINE_NOT_READY, message: "" }),
          ),
      });
      o.signal?.addEventListener("abort", () => {
        done = true;
        stop();
      });
      void (async () => {
        try {
          const res = await deps.fetch(
            `${API}/workloads/${encodeURIComponent(name)}/activate`,
            POST_JSON({ wait: false, force: o.force === true }),
          );
          if (res.status === 200) {
            finish();
            return;
          }
          const body = (await res.json().catch(() => ({}))) as {
            op_id?: string;
          };
          opId = body.op_id ?? res.headers.get("X-LocalAI-Op-Id");
        } catch (e) {
          finish(toTransportError(e, CODE_WORKLOAD_FAILED));
        }
      })();
    });

  const loadModel = (modelId: string, o: ActivateOptions) =>
    new Promise<void>((resolve, reject) => {
      let done = false;
      const finish = (err?: TransportError) => {
        if (done) return;
        done = true;
        stop();
        if (err) reject(err);
        else resolve();
      };
      const stop = follow(deps, {
        types: "model.*",
        events: {
          "model.loading": (f) => {
            if (f.model === modelId)
              o.onProgress?.({
                progress: num(f.progress),
                step: "loading",
                model: modelId,
              });
          },
          "model.loaded": (f) => {
            if (f.model === modelId) finish();
          },
          "model.failed": (f) => {
            if (f.model !== modelId) return;
            finish(
              new TransportError({
                code: str(f.code) ?? CODE_ENGINE_START_FAILED,
                message: str(f.message) ?? "",
              }),
            );
          },
        },
        poll: () => {
          void json<MusicStatus>(deps, `${API}/music/status`).then(
            (s) => {
              if (s.loaded_model === modelId && s.engine_state === "ready")
                finish();
              else if (s.engine_state === "failed")
                finish(
                  new TransportError({
                    code: CODE_ENGINE_START_FAILED,
                    message: "",
                  }),
                );
            },
            () => undefined,
          );
        },
        pollMs: SWITCH_POLL_MS,
        timeoutMs: SWITCH_TIMEOUT_MS,
        onTimeout: () =>
          finish(
            new TransportError({ code: CODE_ENGINE_NOT_READY, message: "" }),
          ),
      });
      o.signal?.addEventListener("abort", () => {
        done = true;
        stop();
      });
      void (async () => {
        try {
          await deps.fetch(
            `${API}/models/${encodeURIComponent(modelId)}/load`,
            POST_JSON({ wait: false }),
          );
        } catch (e) {
          finish(toTransportError(e, CODE_ENGINE_START_FAILED));
        }
      })();
    });

  return {
    list,
    activate: (name, o = {}) => {
      const model = synthetic.get(name);
      return model ? loadModel(model, o) : activateWorkload(name, o);
    },
  };
}

/** The multipart body of POST /music/generate (contract section 3). Optional fields are left out, never sent empty. */
export function generateForm(req: GenerateRequest): FormData {
  const f = new FormData();
  f.set("lyrics", req.lyrics);
  f.set("tags", req.tags);
  f.set("duration_s", String(req.duration_s));
  f.set("language", req.language);
  if (req.seed !== null) f.set("seed", String(req.seed));
  f.set("variations", String(req.variations));
  if (req.sample) {
    f.set("sample", req.sample, req.sample.name);
    if (req.sample_mode) f.set("sample_mode", req.sample_mode);
    if (req.strength !== null) f.set("strength", String(req.strength));
  }
  return f;
}

/** A MusicOp from the route or an event (missing fields get safe values). */
export function toOp(opId: string, f: Record<string, unknown>): MusicOp {
  const state = f.state;
  return {
    op_id: opId,
    state:
      state === "queued" ||
      state === "running" ||
      state === "done" ||
      state === "failed" ||
      state === "cancelled"
        ? state
        : "running",
    progress: num(f.progress),
    song_ids: Array.isArray(f.song_ids)
      ? f.song_ids.filter((x): x is string => typeof x === "string")
      : [],
    error: errorOf(f.error),
  };
}

const ENDED = new Set(["done", "failed", "cancelled"]);

/** agentMusic is the MusicTransport of the agent. */
export function agentMusic(deps: AgentDeps = realDeps()): MusicTransport {
  const songPath = (id: string) =>
    `${API}/music/songs/${encodeURIComponent(id)}`;

  return {
    status: () => json<MusicStatus>(deps, `${API}/music/status`),

    generate: (req) =>
      json<GenerateAccepted>(
        deps,
        `${API}/music/generate`,
        { method: "POST", body: generateForm(req) },
        "LAI-SVC-154",
      ),

    watch(opId, onUpdate) {
      let ended = false;
      const path = `${API}/music/ops/${encodeURIComponent(opId)}`;
      const deliver = (op: MusicOp) => {
        if (ended) return;
        if (ENDED.has(op.state)) {
          ended = true;
          stop();
        }
        onUpdate(op);
      };
      const read = () =>
        json<Record<string, unknown>>(deps, path).then(
          (r) => deliver(toOp(opId, r)),
          () => undefined,
        );
      const stop = follow(deps, {
        types: "music.*",
        events: {
          "music.progress": (f) => {
            if (f.op_id === opId) deliver(toOp(opId, f));
          },
          "music.done": (f) => {
            if (f.op_id !== opId) return;
            // The route has the error and the final song ids: ask it, and use the event if it does not answer.
            void json<Record<string, unknown>>(deps, path).then(
              (r) => deliver(toOp(opId, r)),
              () =>
                deliver(
                  toOp(opId, {
                    ...f,
                    state: ENDED.has(String(f.state)) ? f.state : "done",
                  }),
                ),
            );
          },
        },
        poll: () => void read(),
        pollMs: OP_POLL_MS,
      });
      void read();
      return () => {
        ended = true;
        stop();
      };
    },

    cancel: async (opId) => {
      await json(
        deps,
        `${API}/music/ops/${encodeURIComponent(opId)}/cancel`,
        { method: "POST" },
        CODE_SONG_FAILED,
      );
    },

    listSongs: (o = {}) => {
      const q = new URLSearchParams();
      if (o.limit) q.set("limit", String(o.limit));
      if (o.cursor) q.set("cursor", o.cursor);
      const qs = q.toString();
      return json<SongsPage>(
        deps,
        `${API}/music/songs${qs ? `?${qs}` : ""}`,
      ).then((p) => ({
        songs: p.songs ?? [],
        next_cursor: p.next_cursor ?? null,
      }));
    },

    audioUrl: (song: Song) => `${songPath(song.id)}/audio`,

    deleteSong: async (id, o) => {
      const headers: Record<string, string> = {};
      if (o?.pin) {
        // The PIN becomes a 5-minute grant bound to this session (agent.md section 3.8.3); the PIN itself is sent once.
        const g = await json<{ grant?: string }>(
          deps,
          `${API}/pin/verify`,
          POST_JSON({ pin: o.pin }),
          "LAI-SEC-003",
        );
        if (g.grant) headers["X-LocalAI-Pin-Grant"] = g.grant;
      }
      await json(deps, songPath(id), { method: "DELETE", headers });
    },
  };
}
