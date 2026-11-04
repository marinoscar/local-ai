import { describe, expect, it } from "vitest";
import {
  JPEG_QUALITY,
  MAX_BYTES,
  MAX_SIDE,
  PhotoError,
  preparePhoto,
  targetSize,
  type ImageDeps,
} from "./prepare";

describe("targetSize", () => {
  it("scales a landscape photo to 1024 wide", () => {
    expect(targetSize(4000, 3000)).toEqual({ width: 1024, height: 768 });
  });
  it("scales a portrait photo to 1024 high", () => {
    expect(targetSize(3000, 4000)).toEqual({ width: 768, height: 1024 });
    expect(targetSize(1000, 1600)).toEqual({ width: 640, height: 1024 });
  });
  it("never enlarges a small photo", () => {
    expect(targetSize(800, 600)).toEqual({ width: 800, height: 600 });
    expect(targetSize(1024, 1024)).toEqual({ width: 1024, height: 1024 });
  });
  it("keeps at least one pixel on a very thin photo", () => {
    expect(targetSize(10000, 1)).toEqual({ width: MAX_SIDE, height: 1 });
  });
});

function fakeDeps(over: Partial<ImageDeps> = {}) {
  const calls: { w: number; h: number; q: number }[] = [];
  let closed = 0;
  const deps: ImageDeps = {
    decode: () =>
      Promise.resolve({
        width: 4000,
        height: 3000,
        close: () => {
          closed++;
        },
      }),
    encode: (_img, w, h, q) => {
      calls.push({ w, h, q });
      return Promise.resolve(new Blob(["jpeg"], { type: "image/jpeg" }));
    },
    ...over,
  };
  return { deps, calls, closed: () => closed };
}

describe("preparePhoto", () => {
  it("downscales and encodes at quality 0.85", async () => {
    const f = fakeDeps();
    const out = await preparePhoto(new Blob(["x"]), f.deps);
    expect(f.calls).toEqual([{ w: 1024, h: 768, q: 0.85 }]);
    expect(JPEG_QUALITY).toBe(0.85);
    expect(out.width).toBe(1024);
    expect(out.height).toBe(768);
    expect(out.blob.type).toBe("image/jpeg");
    expect(f.closed()).toBe(1);
  });

  it("accepts a file of exactly 30 MB and rejects one byte more", async () => {
    const f = fakeDeps();
    await expect(
      preparePhoto({ size: MAX_BYTES } as Blob, f.deps),
    ).resolves.toBeDefined();
    await expect(
      preparePhoto({ size: MAX_BYTES + 1 } as Blob, f.deps),
    ).rejects.toMatchObject({ code: "LAI-SVC-108", reason: "too_big" });
    expect(f.calls).toHaveLength(1); // the big one was never decoded
  });

  it("reports a file that does not decode as LAI-SVC-108", async () => {
    const f = fakeDeps({ decode: () => Promise.reject(new Error("bad")) });
    const err = await preparePhoto(new Blob(["not an image"]), f.deps).catch(
      (e: unknown) => e,
    );
    expect(err).toBeInstanceOf(PhotoError);
    expect(err).toMatchObject({ code: "LAI-SVC-108", reason: "not_image" });
  });

  it("reports an empty encode as LAI-SVC-108 and still closes the image", async () => {
    const f = fakeDeps({ encode: () => Promise.resolve(null) });
    await expect(preparePhoto(new Blob(["x"]), f.deps)).rejects.toMatchObject({
      code: "LAI-SVC-108",
    });
    expect(f.closed()).toBe(1);
  });

  it("rejects a zero-size image", async () => {
    const f = fakeDeps({ decode: () => Promise.resolve({ width: 0, height: 5 }) });
    await expect(preparePhoto(new Blob(["x"]), f.deps)).rejects.toBeInstanceOf(
      PhotoError,
    );
  });
});
