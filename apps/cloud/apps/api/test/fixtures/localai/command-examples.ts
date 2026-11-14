// Loads `commands.examples.json` (one valid params and result per command type). A sha256 in it is written as a
// placeholder, `"<sha256:N>"`, which this loader expands to N repeated 64 times: the public repository's redaction
// check (`scripts/check-redaction.sh`) refuses any literal 64-hex string, and a digest of nothing real is still a
// valid shape for the strict schemas. Every reader of the examples goes through here.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const FILE = join(__dirname, 'commands.examples.json');

/** The examples with the placeholders expanded. */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function readCommandExamples<T = Record<string, { params: any; result: any }>>(): T {
  const text = readFileSync(FILE, 'utf8').replace(/"<sha256:([0-9a-f])>"/g, (_match, digit: string) => `"${digit.repeat(64)}"`);
  return JSON.parse(text) as T;
}
