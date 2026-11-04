// Planned download sizes for the Models tab (docs/specs/cloud-app.md section 3.11: "Download 5.1 GB"). The size lives in
// the approved catalog's `models.yaml` (`files[].size_bytes`), not in the node's snapshot, so the detail route reads it
// from the stored catalog text. The catalog was linted by the real `aictl` before it could be approved, so the layout
// is the documented one (a `models:` list of block mappings); this reads only `id` and `size_bytes` lines and is
// deliberately forgiving: a model it cannot size is simply left out and the UI says the node reports the size itself.
// Pure: no I/O.

const MODEL_ID = /^\s*-\s+id:\s*["']?([a-z0-9][a-z0-9.\-]{2,63})["']?\s*(?:#.*)?$/;
const SIZE_BYTES = /^\s+(?:-\s+)?size_bytes:\s*(\d+)\s*(?:#.*)?$/;
/** A top-level key other than `models:` ends the list. */
const TOP_LEVEL_KEY = /^[A-Za-z_][\w-]*:/;

/** model id -> total bytes of its files (sum of `size_bytes`), from the text of `models.yaml`. */
export function modelSizesFromYaml(yaml: string): Record<string, number> {
  const sizes: Record<string, number> = {};
  let current: string | null = null;
  let inModels = false;
  for (const line of yaml.split(/\r?\n/)) {
    if (TOP_LEVEL_KEY.test(line)) {
      inModels = line.startsWith('models:');
      current = null;
      continue;
    }
    if (!inModels) continue;
    const id = MODEL_ID.exec(line);
    if (id) {
      current = id[1]!;
      continue;
    }
    const size = SIZE_BYTES.exec(line);
    if (size && current) {
      const bytes = Number(size[1]);
      if (Number.isSafeInteger(bytes)) sizes[current] = (sizes[current] ?? 0) + bytes;
    }
  }
  return sizes;
}

/** The sizes in a stored catalog's `files` JSON (an array of `{ path, content }`), or `{}` when there is no `models.yaml`. */
export function modelSizesFromFiles(files: unknown): Record<string, number> {
  if (!Array.isArray(files)) return {};
  for (const file of files) {
    if (file && typeof file === 'object' && (file as { path?: unknown }).path === 'models.yaml' && typeof (file as { content?: unknown }).content === 'string') {
      return modelSizesFromYaml((file as { content: string }).content);
    }
  }
  return {};
}
