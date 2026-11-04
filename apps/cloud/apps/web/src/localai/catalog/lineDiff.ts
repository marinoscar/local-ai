// A small dependency-free line diff (Myers) and the unified rendering of it. The Catalog page diffs the files of two
// versions in the browser: the API answers which files differ by digest (`?diff=approved`); the lines are this file's.
//
// Files are small (a catalog is at most 2 MB), but a quadratic table would still be slow on a big YAML, so common head
// and tail lines are trimmed first and the search is the O(ND) Myers walk with a ceiling: past it, the middle part is
// shown as one removed block and one added block (correct, just less minimal).

export type LineOp = { kind: 'equal' | 'add' | 'remove'; text: string };

/** The most edit distance the search explores before it gives up on a minimal diff. */
const MAX_EDIT_DISTANCE = 1000;

/** Splits a text into lines; a trailing newline does not make an extra empty line. "" is no lines. */
export function splitLines(text: string): string[] {
  if (text === '') return [];
  const lines = text.split(/\r?\n/);
  if (lines[lines.length - 1] === '') lines.pop();
  return lines;
}

function myers(a: readonly string[], b: readonly string[]): LineOp[] | null {
  const n = a.length;
  const m = b.length;
  const max = Math.min(n + m, MAX_EDIT_DISTANCE);
  const offset = max + 1;
  let v = new Int32Array(2 * max + 3);
  const trace: Int32Array[] = [];
  let found = -1;
  for (let d = 0; d <= max && found < 0; d += 1) {
    trace.push(v.slice());
    for (let k = -d; k <= d; k += 2) {
      let x: number;
      if (k === -d || (k !== d && v[offset + k - 1]! < v[offset + k + 1]!)) x = v[offset + k + 1]!;
      else x = v[offset + k - 1]! + 1;
      let y = x - k;
      while (x < n && y < m && a[x] === b[y]) {
        x += 1;
        y += 1;
      }
      v[offset + k] = x;
      if (x >= n && y >= m) {
        found = d;
        break;
      }
    }
  }
  if (found < 0) return null;

  // Walk the trace backwards into edits.
  const ops: LineOp[] = [];
  let x = n;
  let y = m;
  for (let d = found; d > 0; d -= 1) {
    const prev = trace[d]!;
    const k = x - y;
    const down = k === -d || (k !== d && prev[offset + k - 1]! < prev[offset + k + 1]!);
    const prevK = down ? k + 1 : k - 1;
    const prevX = prev[offset + prevK]!;
    const prevY = prevX - prevK;
    while (x > prevX && y > prevY) {
      x -= 1;
      y -= 1;
      ops.push({ kind: 'equal', text: a[x]! });
    }
    if (down) {
      y -= 1;
      ops.push({ kind: 'add', text: b[y]! });
    } else {
      x -= 1;
      ops.push({ kind: 'remove', text: a[x]! });
    }
  }
  while (x > 0 && y > 0) {
    x -= 1;
    y -= 1;
    ops.push({ kind: 'equal', text: a[x]! });
  }
  return ops.reverse();
}

/** The line edits turning `before` into `after`. */
export function diffLines(before: readonly string[], after: readonly string[]): LineOp[] {
  let head = 0;
  while (head < before.length && head < after.length && before[head] === after[head]) head += 1;
  let tail = 0;
  while (tail < before.length - head && tail < after.length - head && before[before.length - 1 - tail] === after[after.length - 1 - tail]) tail += 1;

  const midBefore = before.slice(head, before.length - tail);
  const midAfter = after.slice(head, after.length - tail);
  const middle =
    myers(midBefore, midAfter) ?? [
      ...midBefore.map((text): LineOp => ({ kind: 'remove', text })),
      ...midAfter.map((text): LineOp => ({ kind: 'add', text })),
    ];
  return [
    ...before.slice(0, head).map((text): LineOp => ({ kind: 'equal', text })),
    ...middle,
    ...before.slice(before.length - tail).map((text): LineOp => ({ kind: 'equal', text })),
  ];
}

export interface DiffLine {
  kind: 'equal' | 'add' | 'remove';
  text: string;
  /** 1-based line number in the old text (null for an added line). */
  oldNo: number | null;
  /** 1-based line number in the new text (null for a removed line). */
  newNo: number | null;
}

export interface Hunk {
  oldStart: number;
  oldCount: number;
  newStart: number;
  newCount: number;
  lines: DiffLine[];
}

export interface FileLineDiff {
  hunks: Hunk[];
  added: number;
  removed: number;
}

/** Groups the edits into unified-diff hunks with `context` unchanged lines around each change. */
export function unifiedHunks(before: string, after: string, context = 3): FileLineDiff {
  const ops = diffLines(splitLines(before), splitLines(after));
  const lines: DiffLine[] = [];
  let oldNo = 1;
  let newNo = 1;
  let added = 0;
  let removed = 0;
  for (const op of ops) {
    if (op.kind === 'equal') {
      lines.push({ kind: 'equal', text: op.text, oldNo, newNo });
      oldNo += 1;
      newNo += 1;
    } else if (op.kind === 'add') {
      lines.push({ kind: 'add', text: op.text, oldNo: null, newNo });
      newNo += 1;
      added += 1;
    } else {
      lines.push({ kind: 'remove', text: op.text, oldNo, newNo: null });
      oldNo += 1;
      removed += 1;
    }
  }
  if (added === 0 && removed === 0) return { hunks: [], added, removed };

  // Mark which lines are shown: every change and `context` lines either side.
  const keep = new Array<boolean>(lines.length).fill(false);
  lines.forEach((line, index) => {
    if (line.kind === 'equal') return;
    for (let i = Math.max(0, index - context); i <= Math.min(lines.length - 1, index + context); i += 1) keep[i] = true;
  });
  const hunks: Hunk[] = [];
  let current: DiffLine[] | null = null;
  const close = () => {
    if (!current) return;
    const firstOld = current.find((line) => line.oldNo !== null)?.oldNo ?? 0;
    const firstNew = current.find((line) => line.newNo !== null)?.newNo ?? 0;
    hunks.push({
      oldStart: firstOld,
      oldCount: current.filter((line) => line.oldNo !== null).length,
      newStart: firstNew,
      newCount: current.filter((line) => line.newNo !== null).length,
      lines: current,
    });
    current = null;
  };
  lines.forEach((line, index) => {
    if (keep[index]) {
      current ??= [];
      current.push(line);
    } else {
      close();
    }
  });
  close();
  return { hunks, added, removed };
}

/** `@@ -3,4 +3,5 @@` */
export function hunkHeader(hunk: Hunk): string {
  return `@@ -${hunk.oldStart},${hunk.oldCount} +${hunk.newStart},${hunk.newCount} @@`;
}

/** A hunk as plain unified-diff text (what a screen reader and a copy-paste get). */
export function hunkText(hunk: Hunk): string {
  const sign = { equal: ' ', add: '+', remove: '-' } as const;
  return [hunkHeader(hunk), ...hunk.lines.map((line) => `${sign[line.kind]}${line.text}`)].join('\n');
}
