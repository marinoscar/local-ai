// Photo preparation (survival-portal.md section 4.6 step 2): decode with the EXIF rotation applied,
// downscale so the long side is at most 1024 px, and re-encode as JPEG at quality 0.85. A canvas
// re-encode carries no metadata, so EXIF (including GPS) is gone by construction. The photo never
// leaves memory and is never stored.

export const MAX_BYTES = 30 * 1024 * 1024;
export const MAX_SIDE = 1024;
export const JPEG_QUALITY = 0.85;

/** PhotoError is a photo that can't be used (LAI-SVC-108): too big, not an image, or not decodable. */
export class PhotoError extends Error {
  readonly code = "LAI-SVC-108";
  readonly reason: "too_big" | "not_image";
  constructor(reason: "too_big" | "not_image") {
    super(reason);
    this.reason = reason;
  }
}

/** targetSize fits (width, height) into a box of max x max without changing the aspect and without enlarging. */
export function targetSize(
  width: number,
  height: number,
  max = MAX_SIDE,
): { width: number; height: number } {
  const long = Math.max(width, height);
  if (long <= max) return { width, height };
  const k = max / long;
  return {
    width: Math.max(1, Math.round(width * k)),
    height: Math.max(1, Math.round(height * k)),
  };
}

/** What the browser parts give: an upright decoded image and a JPEG encoder. Injected so tests need no browser. */
export interface ImageDeps {
  decode(file: Blob): Promise<{ width: number; height: number; close?(): void }>;
  encode(
    img: { width: number; height: number },
    width: number,
    height: number,
    quality: number,
  ): Promise<Blob | null>;
}

export interface PreparedPhoto {
  blob: Blob;
  width: number;
  height: number;
}

/**
 * preparePhoto validates and converts a file. It throws PhotoError for a file over 30 MB, one that
 * can't be decoded as an image, or an encode that produced nothing.
 */
export async function preparePhoto(
  file: Blob,
  deps: ImageDeps = browserImageDeps,
): Promise<PreparedPhoto> {
  if (file.size > MAX_BYTES) throw new PhotoError("too_big");
  let img: Awaited<ReturnType<ImageDeps["decode"]>>;
  try {
    img = await deps.decode(file);
  } catch {
    throw new PhotoError("not_image");
  }
  try {
    if (!(img.width > 0 && img.height > 0)) throw new PhotoError("not_image");
    const size = targetSize(img.width, img.height);
    const blob = await deps.encode(img, size.width, size.height, JPEG_QUALITY);
    if (!blob || blob.size === 0) throw new PhotoError("not_image");
    return { blob, width: size.width, height: size.height };
  } finally {
    img.close?.();
  }
}

export const browserImageDeps: ImageDeps = {
  decode: (file) => createImageBitmap(file, { imageOrientation: "from-image" }),
  encode: (img, width, height, quality) =>
    new Promise((resolve) => {
      const canvas = document.createElement("canvas");
      canvas.width = width;
      canvas.height = height;
      const ctx = canvas.getContext("2d");
      if (!ctx) {
        resolve(null);
        return;
      }
      ctx.imageSmoothingQuality = "high";
      ctx.drawImage(img as ImageBitmap, 0, 0, width, height);
      canvas.toBlob((b) => resolve(b), "image/jpeg", quality);
    }),
};

/** toDataUrl is the base64 data URL of the request (FileReader: no network, no disk). */
export function toDataUrl(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result));
    r.onerror = () => reject(new Error("read failed"));
    r.readAsDataURL(blob);
  });
}
