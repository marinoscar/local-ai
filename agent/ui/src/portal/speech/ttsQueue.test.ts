import { describe, expect, it } from "vitest";
import { AHEAD, replay, TtsQueue, type TtsDeps } from "./ttsQueue";

const wav = (name: string) => new Blob([name], { type: "audio/wav" });

interface Pending {
  text: string;
  resolve: (w: Blob[] | null) => void;
  reject: (e: unknown) => void;
  signal: AbortSignal;
}

/** A fake deps: requests wait until the test resolves them; playback waits until released. */
function fakeDeps() {
  const requests: Pending[] = [];
  const played: string[] = [];
  const playing: { name: string; release: () => void }[] = [];
  const deps: TtsDeps = {
    fetchWav(text, signal) {
      return new Promise((resolve, reject) => {
        requests.push({ text, resolve, reject, signal });
        signal.addEventListener("abort", () =>
          reject(new DOMException("aborted", "AbortError")),
        );
      });
    },
    play(w, signal) {
      return new Promise<void>((resolve) => {
        const name = String(w.size);
        played.push(name);
        playing.push({ name, release: resolve });
        signal.addEventListener("abort", () => resolve());
      });
    },
  };
  return { deps, requests, played, playing };
}

const tick = () => new Promise((r) => setTimeout(r, 0));

describe("TtsQueue", () => {
  it("keeps at most AHEAD requests beyond the sentence being played", async () => {
    const f = fakeDeps();
    const q = new TtsQueue(f.deps);
    for (const s of ["one", "two", "three", "four", "five"]) q.enqueue(s);
    await tick();
    expect(f.requests.map((r) => r.text)).toEqual(["one", "two", "three"]);
    expect(f.requests.length).toBe(AHEAD + 1);
    // The first one is ready and starts playing: the window slides by one.
    f.requests[0].resolve([wav("a")]);
    await tick();
    f.playing[0].release();
    await tick();
    expect(f.requests.map((r) => r.text)).toEqual([
      "one",
      "two",
      "three",
      "four",
    ]);
    q.stop();
  });

  it("plays in sentence order even when later audio is ready first", async () => {
    const f = fakeDeps();
    const q = new TtsQueue(f.deps);
    q.enqueue("one");
    q.enqueue("two");
    await tick();
    f.requests[1].resolve([new Blob(["22"])]);
    await tick();
    expect(f.played).toEqual([]); // two is ready but one is not
    f.requests[0].resolve([new Blob(["1"])]);
    await tick();
    expect(f.played).toEqual(["1"]);
    f.playing[0].release();
    await tick();
    expect(f.played).toEqual(["1", "2"]);
    f.playing[1].release();
    q.end();
    await q.finished;
  });

  it("calls onSpeak once, before the first sound", async () => {
    const f = fakeDeps();
    let n = 0;
    const q = new TtsQueue(f.deps, { onSpeak: () => n++ });
    q.enqueue("one");
    q.enqueue("two");
    await tick();
    f.requests[0].resolve([wav("a")]);
    f.requests[1].resolve([wav("b")]);
    await tick();
    f.playing[0].release();
    await tick();
    f.playing[1].release();
    q.end();
    await q.finished;
    expect(n).toBe(1);
    expect(q.cached).toHaveLength(2);
  });

  it("stop aborts every request and the sound", async () => {
    const f = fakeDeps();
    const q = new TtsQueue(f.deps);
    q.enqueue("one");
    q.enqueue("two");
    q.enqueue("three");
    await tick();
    f.requests[0].resolve([wav("a")]);
    await tick();
    q.stop();
    await q.finished;
    expect(f.requests[1].signal.aborted).toBe(true);
    expect(f.requests[2].signal.aborted).toBe(true);
    expect(f.playing).toHaveLength(1);
    q.enqueue("late");
    await tick();
    expect(f.requests).toHaveLength(3); // nothing new after stop
  });

  it("goes text-only when there is no voice, once, and stops asking", async () => {
    const f = fakeDeps();
    let textOnly = 0;
    const q = new TtsQueue(f.deps, { onTextOnly: () => textOnly++ });
    q.enqueue("one");
    q.enqueue("two");
    await tick();
    f.requests[0].resolve(null);
    f.requests[1].resolve(null);
    await tick();
    q.enqueue("three");
    q.end();
    await q.finished;
    expect(textOnly).toBe(1);
    expect(q.isTextOnly).toBe(true);
    expect(f.played).toEqual([]);
    expect(f.requests).toHaveLength(2); // "three" was never requested
  });

  it("reports a failed request once and skips that sentence", async () => {
    const f = fakeDeps();
    const errs: unknown[] = [];
    const q = new TtsQueue(f.deps, { onError: (e) => errs.push(e) });
    q.enqueue("one");
    q.enqueue("two");
    await tick();
    f.requests[0].reject(new Error("boom"));
    f.requests[1].resolve([new Blob(["2"])]);
    await tick();
    expect(f.played).toEqual(["1"]); // "two": 1 byte
    f.playing[0].release();
    q.end();
    await q.finished;
    expect(errs).toHaveLength(1);
  });

  it("finishes at once when ended with nothing queued", async () => {
    const q = new TtsQueue(fakeDeps().deps);
    q.end();
    await q.finished;
  });

  it("ignores blank sentences", async () => {
    const f = fakeDeps();
    const q = new TtsQueue(f.deps);
    q.enqueue("   ");
    q.end();
    await q.finished;
    expect(f.requests).toHaveLength(0);
  });
});

describe("replay", () => {
  it("plays the cache in order with no request", async () => {
    const order: string[] = [];
    const ctl = new AbortController();
    await replay(
      [new Blob(["a"]), new Blob(["bb"])],
      (w) => {
        order.push(String(w.size));
        return Promise.resolve();
      },
      ctl.signal,
    );
    expect(order).toEqual(["1", "2"]);
  });
  it("stops when aborted", async () => {
    const ctl = new AbortController();
    const order: string[] = [];
    await replay(
      [new Blob(["a"]), new Blob(["bb"])],
      (w) => {
        order.push(String(w.size));
        ctl.abort();
        return Promise.resolve();
      },
      ctl.signal,
    );
    expect(order).toEqual(["1"]);
  });
});
