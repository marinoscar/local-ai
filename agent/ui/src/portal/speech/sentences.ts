// Sentence splitter for spoken answers (survival-portal.md section 4.5 step 5). A sentence ends at
// one of . ! ? ¿ ¡ … (with any closing quote or bracket after it) followed by white space, or is cut
// at MAX_SENTENCE characters at the last space. It does not split after an abbreviation (Dr., Sra.,
// p. ej., e.g., etc.) or inside a number (1.5), and it works on a stream: push() the text as it
// arrives and take the sentences that are complete.

export const MAX_SENTENCE = 220;

const TERMINATORS = new Set([".", "!", "?", "¿", "¡", "…"]);
const CLOSERS = new Set(['"', "'", ")", "]", "»", "”", "’"]);

// Lower case, without the final period. Single letters (the "p" of "p. ej.") are handled apart.
const ABBREVIATIONS = new Set([
  "dr",
  "dra",
  "sr",
  "sra",
  "srta",
  "sres",
  "lic",
  "ing",
  "prof",
  "etc",
  "ej",
  "e.g",
  "i.e",
  "vs",
  "mr",
  "mrs",
  "ms",
  "a.m",
  "p.m",
  "núm",
  "aprox",
  "pág",
  "fig",
]);

function isSpace(c: string | undefined): boolean {
  return c !== undefined && /\s/.test(c);
}

/** isAbbreviation: the word that ends just before the period at index i is a known abbreviation. */
function isAbbreviation(s: string, i: number): boolean {
  const m = /([\p{L}.]+)$/u.exec(s.slice(0, i));
  if (!m) return false;
  const w = m[1].toLowerCase();
  return ABBREVIATIONS.has(w) || /^\p{Ll}$/u.test(w);
}

/**
 * findBoundary is the index just after the first sentence end of s (terminators and closers
 * included, the white space after them not), or -1 when s holds no complete sentence yet.
 */
function findBoundary(s: string): number {
  for (let i = 0; i < s.length; i++) {
    if (!TERMINATORS.has(s[i])) continue;
    let j = i;
    while (j + 1 < s.length && (TERMINATORS.has(s[j + 1]) || CLOSERS.has(s[j + 1])))
      j++;
    if (j + 1 >= s.length) return -1; // need the next character to know
    if (isSpace(s[j + 1])) {
      if (s[i] === "." && j === i && isAbbreviation(s, i)) {
        i = j;
        continue;
      }
      return j + 1;
    }
    i = j;
  }
  return -1;
}

/** SentenceSplitter turns a stream of text into sentences. */
export class SentenceSplitter {
  private buf = "";

  /** push adds text and returns the sentences that are now complete (possibly none). */
  push(chunk: string): string[] {
    this.buf += chunk;
    const out: string[] = [];
    for (;;) {
      const b = findBoundary(this.buf);
      if (b >= 0 && b <= MAX_SENTENCE) {
        const s = this.buf.slice(0, b).trim();
        this.buf = this.buf.slice(b).trimStart();
        if (s) out.push(s);
      } else if (this.buf.length > MAX_SENTENCE) {
        let cut = this.buf.lastIndexOf(" ", MAX_SENTENCE);
        if (cut <= 0) cut = MAX_SENTENCE;
        const s = this.buf.slice(0, cut).trim();
        this.buf = this.buf.slice(cut).trimStart();
        if (s) out.push(s);
      } else {
        return out;
      }
    }
  }

  /** flush returns what is left as a last sentence (the end of the answer). */
  flush(): string[] {
    const rest = this.buf.trim();
    this.buf = "";
    return rest ? [rest] : [];
  }
}

/** splitSentences splits a whole text (used by Read aloud). */
export function splitSentences(text: string): string[] {
  const sp = new SentenceSplitter();
  return [...sp.push(text), ...sp.flush()];
}
