// The help text uses three marks (survival-portal.md section 4.9): **bold**, *italic* and `code`.
// This turns one string into plain segments so the screen can render them without innerHTML.

export type Segment = { kind: "text" | "strong" | "em" | "code"; text: string };

const MARK = /\*\*([^*]+)\*\*|`([^`]+)`|\*([^*]+)\*/g;

export function parseInline(src: string): Segment[] {
  const out: Segment[] = [];
  let last = 0;
  for (const m of src.matchAll(MARK)) {
    if (m.index > last) out.push({ kind: "text", text: src.slice(last, m.index) });
    if (m[1] !== undefined) out.push({ kind: "strong", text: m[1] });
    else if (m[2] !== undefined) out.push({ kind: "code", text: m[2] });
    else out.push({ kind: "em", text: m[3] });
    last = m.index + m[0].length;
  }
  if (last < src.length) out.push({ kind: "text", text: src.slice(last) });
  return out;
}
