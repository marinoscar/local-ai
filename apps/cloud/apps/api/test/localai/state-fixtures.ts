// Shared helpers of the state-ingestion specs: the fixtures the Go agent replays too, and the secret-looking strings
// built AT TEST TIME. A tailnet IP, a token or a key is never a literal in this repository (the redaction check, and
// gitleaks, would fail the build); `lai-secret` strings are assembled from parts so no source line contains one.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import type { LocalaiStateSnapshotV1 } from '../../src/localai/contracts/state.schema';

const DIR = join(__dirname, '../fixtures/localai');

export function loadFixture<T>(name: string): T {
  return JSON.parse(readFileSync(join(DIR, name), 'utf8')) as T;
}

/** A fresh deep copy of the full ai-n3 snapshot. */
export const ai3 = (): LocalaiStateSnapshotV1 => loadFixture<LocalaiStateSnapshotV1>('state.ai-n3.json');

/** The three-node lab: ai-n1, ai-n2 (offline mode), ai-n3. */
export const lab3 = (): LocalaiStateSnapshotV1[] => loadFixture<LocalaiStateSnapshotV1[]>('state.lab-3-nodes.json');

/** The secret-looking strings (none is a literal). */
export const secrets = {
  tailnetIp: () => [100, 101, 1, 2].join('.'),
  tailnetIpLow: () => [100, 64, 0, 9].join('.'),
  lanIp: () => [192, 168, 1, 20].join('.'),
  privateTen: () => [10, 20, 30, 40].join('.'),
  private172: () => [172, 16, 5, 6].join('.'),
  nodeToken: () => 'nod' + '_' + 'A'.repeat(43),
  labToken: () => 'lat' + '_' + 'b'.repeat(43),
  patToken: () => 'pat' + '_' + 'c'.repeat(30),
  laiToken: () => 'lai' + '_' + 'd'.repeat(24),
  hexKey: () => 'ab12cd34'.repeat(8),
  homePath: () => ['', 'home', 'person-a', 'models'].join('/'),
};
