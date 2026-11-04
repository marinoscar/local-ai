import { diffCatalogFiles, summarizeDiff, summaryText } from './catalogDiff';
import { diffLines, hunkHeader, hunkText, splitLines, unifiedHunks, type LineOp } from './lineDiff';

/** Applying the edits to `before` must give `after`, and the equal/remove lines must be exactly `before`. */
function check(before: string[], after: string[]): LineOp[] {
  const ops = diffLines(before, after);
  expect(ops.filter((op) => op.kind !== 'add').map((op) => op.text)).toEqual(before);
  expect(ops.filter((op) => op.kind !== 'remove').map((op) => op.text)).toEqual(after);
  return ops;
}

describe('splitLines', () => {
  it('drops the empty line a trailing newline makes, keeps blank lines inside, handles CRLF and the empty text', () => {
    expect(splitLines('')).toEqual([]);
    expect(splitLines('a\nb\n')).toEqual(['a', 'b']);
    expect(splitLines('a\n\nb')).toEqual(['a', '', 'b']);
    expect(splitLines('a\r\nb\r\n')).toEqual(['a', 'b']);
    expect(splitLines('\n')).toEqual(['']);
  });
});

describe('diffLines', () => {
  it('finds nothing to change in equal texts, and everything in a text against nothing', () => {
    expect(check(['a', 'b'], ['a', 'b']).every((op) => op.kind === 'equal')).toBe(true);
    expect(check([], ['a', 'b']).map((op) => op.kind)).toEqual(['add', 'add']);
    expect(check(['a', 'b'], []).map((op) => op.kind)).toEqual(['remove', 'remove']);
    expect(check([], [])).toEqual([]);
  });

  it('is minimal for a changed, an added and a removed line', () => {
    expect(check(['a', 'b', 'c'], ['a', 'B', 'c']).filter((op) => op.kind !== 'equal')).toEqual([
      { kind: 'remove', text: 'b' },
      { kind: 'add', text: 'B' },
    ]);
    expect(check(['a', 'c'], ['a', 'b', 'c']).filter((op) => op.kind !== 'equal')).toEqual([{ kind: 'add', text: 'b' }]);
    expect(check(['a', 'b', 'c'], ['a', 'c']).filter((op) => op.kind !== 'equal')).toEqual([{ kind: 'remove', text: 'b' }]);
  });

  it('keeps repeated lines in order (the classic ABCABBA to CBABAC case has 5 edits)', () => {
    const ops = check([...'ABCABBA'], [...'CBABAC']);
    expect(ops.filter((op) => op.kind !== 'equal')).toHaveLength(5);
  });

  it('reconstructs both sides for 300 random pairs', () => {
    let seed = 12345;
    const next = () => {
      seed = (seed * 1103515245 + 12345) & 0x7fffffff;
      return seed;
    };
    for (let round = 0; round < 300; round += 1) {
      const make = () => Array.from({ length: next() % 14 }, () => String.fromCharCode(97 + (next() % 4)));
      check(make(), make());
    }
  });

  it('handles a big file with a few edits quickly', () => {
    const before = Array.from({ length: 20000 }, (_, i) => `line ${i}`);
    const after = [...before];
    after[100] = 'changed';
    after.splice(15000, 1);
    after.splice(19000, 0, 'inserted');
    const started = Date.now();
    const ops = check(before, after);
    expect(ops.filter((op) => op.kind !== 'equal')).toHaveLength(4);
    expect(Date.now() - started).toBeLessThan(2000);
  });

  it('still returns a correct (if not minimal) answer past the search ceiling', () => {
    const before = Array.from({ length: 1500 }, (_, i) => `a${i}`);
    const after = Array.from({ length: 1500 }, (_, i) => `b${i}`);
    check(before, after);
  });
});

describe('unifiedHunks', () => {
  const before = ['1', '2', '3', '4', '5', '6', '7', '8', '9', '10'].join('\n');

  it('has no hunks for equal texts', () => {
    expect(unifiedHunks(before, before)).toEqual({ hunks: [], added: 0, removed: 0 });
  });

  it('shows three lines of context and numbers both sides', () => {
    const after = before.replace('\n5\n', '\nFIVE\n');
    const { hunks, added, removed } = unifiedHunks(before, after);
    expect([added, removed]).toEqual([1, 1]);
    expect(hunks).toHaveLength(1);
    expect(hunkHeader(hunks[0]!)).toBe('@@ -2,7 +2,7 @@');
    expect(hunkText(hunks[0]!)).toBe(['@@ -2,7 +2,7 @@', ' 2', ' 3', ' 4', '-5', '+FIVE', ' 6', ' 7', ' 8'].join('\n'));
    const change = hunks[0]!.lines.filter((line) => line.kind !== 'equal');
    expect(change).toEqual([
      { kind: 'remove', text: '5', oldNo: 5, newNo: null },
      { kind: 'add', text: 'FIVE', oldNo: null, newNo: 5 },
    ]);
  });

  it('splits distant changes into separate hunks and merges near ones', () => {
    const lines = Array.from({ length: 30 }, (_, i) => `l${i + 1}`);
    const far = [...lines];
    far[1] = 'X';
    far[27] = 'Y';
    expect(unifiedHunks(lines.join('\n'), far.join('\n')).hunks).toHaveLength(2);
    const near = [...lines];
    near[1] = 'X';
    near[6] = 'Y';
    expect(unifiedHunks(lines.join('\n'), near.join('\n')).hunks).toHaveLength(1);
  });

  it('describes an added file as one hunk of additions from line 1', () => {
    const { hunks, added, removed } = unifiedHunks('', 'a\nb\n');
    expect([added, removed]).toEqual([2, 0]);
    expect(hunkHeader(hunks[0]!)).toBe('@@ -0,0 +1,2 @@');
  });
});

describe('diffCatalogFiles', () => {
  const file = (path: string, content: string) => ({ path, content, sha256: `sha-${content}` });

  it('classifies each file and summarises the totals', () => {
    const base = [file('CATALOG_VERSION', '2026.11.02-1\n'), file('models.yaml', 'a\nb\n'), file('ark.yaml', 'same\n'), file('profiles/old.yaml', 'x\n')];
    const target = [file('CATALOG_VERSION', '2026.11.03-1\n'), file('models.yaml', 'a\nB\nc\n'), file('ark.yaml', 'same\n'), file('profiles/new.yaml', 'y\nz\n')];
    const files = diffCatalogFiles(base, target);
    expect(files.map((item) => [item.path, item.change, item.added, item.removed])).toEqual([
      ['CATALOG_VERSION', 'changed', 1, 1],
      ['models.yaml', 'changed', 2, 1],
      ['ark.yaml', 'unchanged', 0, 0],
      ['profiles/new.yaml', 'added', 2, 0],
      ['profiles/old.yaml', 'removed', 0, 1],
    ]);
    const summary = summarizeDiff(files);
    expect(summary).toEqual({ filesChanged: 4, added: 5, removed: 3 });
    expect(summaryText(summary)).toBe('4 files changed, +5 -3 lines');
  });

  it('says "No changes" and uses the singular', () => {
    expect(summaryText({ filesChanged: 0, added: 0, removed: 0 })).toBe('No changes');
    expect(summaryText({ filesChanged: 1, added: 1, removed: 0 })).toBe('1 file changed, +1 -0 line');
  });
});
