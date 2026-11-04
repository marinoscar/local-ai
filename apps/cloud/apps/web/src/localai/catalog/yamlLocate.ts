// "Click a lint problem, land on its line." A lint problem names a YAML path (`models[3].source.revision`); the editor
// is a plain textarea, so the line is found by text search, best effort: walk the path key by key (list items by
// position), and when the walk loses the thread fall back to the first line that holds the path's last key. Never
// throws; null means "no idea", and the caller still opens the file.

type Token = { key: string } | { index: number };

/** `models[3].source.revision` -> models, 3, source, revision. */
export function tokenizePath(path: string): Token[] {
  const tokens: Token[] = [];
  for (const part of path.split('.')) {
    const match = /^([^[\]]*)((?:\[\d+\])*)$/.exec(part);
    if (!match) {
      if (part) tokens.push({ key: part });
      continue;
    }
    if (match[1]) tokens.push({ key: match[1] });
    for (const index of match[2]!.matchAll(/\[(\d+)\]/g)) tokens.push({ index: Number(index[1]) });
  }
  return tokens;
}

const escapeRegExp = (text: string) => text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
const indentOf = (line: string) => line.length - line.trimStart().length;
const isBlank = (line: string) => line.trim() === '' || line.trimStart().startsWith('#');

/** The 1-based line of `path` in `content`, or null. */
export function locateYamlPath(content: string, path: string): number | null {
  const tokens = tokenizePath(path);
  if (tokens.length === 0) return null;
  const lines = content.split(/\r?\n/);

  // Walk: each key is searched from the previous match on, among lines indented deeper than the container (or equal
  // for the first key); each index counts list items ("- ") directly below the previous key.
  let position = 0;
  let floor = -1;
  let ok = true;
  for (const token of tokens) {
    if ('key' in token) {
      const re = new RegExp(`^(\\s*)(?:-\\s+)?${escapeRegExp(token.key)}\\s*:`);
      let found = -1;
      for (let i = position; i < lines.length; i += 1) {
        const line = lines[i]!;
        if (isBlank(line)) continue;
        // Leaving the container ends the search.
        if (floor >= 0 && i > position && indentOf(line) <= floor && !/^\s*-\s/.test(line)) break;
        const hit = re.exec(line);
        if (hit && (i === position ? true : indentOf(line) > floor)) {
          found = i;
          break;
        }
      }
      if (found < 0) {
        ok = false;
        break;
      }
      position = found;
      floor = indentOf(lines[found]!);
    } else {
      let seen = -1;
      let itemIndent = -1;
      let found = -1;
      for (let i = position + 1; i < lines.length; i += 1) {
        const line = lines[i]!;
        if (isBlank(line)) continue;
        const dash = /^(\s*)-(\s|$)/.exec(line);
        if (itemIndent < 0) {
          if (!dash || indentOf(line) < floor) break;
          itemIndent = indentOf(line);
        }
        if (indentOf(line) < itemIndent) break;
        if (dash && indentOf(line) === itemIndent) {
          seen += 1;
          if (seen === token.index) {
            found = i;
            break;
          }
        }
      }
      if (found < 0) {
        ok = false;
        break;
      }
      position = found;
      floor = itemIndent;
    }
  }
  if (ok) return position + 1;

  // Fallback: the last key anywhere.
  const lastKey = [...tokens].reverse().find((token): token is { key: string } => 'key' in token);
  if (!lastKey) return null;
  const re = new RegExp(`^\\s*(?:-\\s+)?${escapeRegExp(lastKey.key)}\\s*:`);
  const index = lines.findIndex((line) => re.test(line));
  return index < 0 ? null : index + 1;
}
