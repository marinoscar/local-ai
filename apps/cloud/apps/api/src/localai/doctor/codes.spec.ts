// The code map equals the registry: the same title and fix as the row of docs/specs/doctor-codes.md, the same severity.
// This is what keeps the Doctor and the alerts (CLOUD-18) from drifting from the spec's wording.
import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

import { CLOUD_CODES } from './codes';

// apps/cloud/apps/api/src/localai/doctor -> the repository root.
const REGISTRY = join(__dirname, '..', '..', '..', '..', '..', '..', '..', 'docs', 'specs', 'doctor-codes.md');

function registryRows(): Map<string, { severity: string; meaning: string; fix: string }> {
  const rows = new Map<string, { severity: string; meaning: string; fix: string }>();
  for (const line of readFileSync(REGISTRY, 'utf8').split('\n')) {
    const cells = line.split('|').map((cell) => cell.trim());
    if (/^LAI-[A-Z]+-\d{3}$/.test(cells[1] ?? '')) rows.set(cells[1], { severity: cells[2], meaning: cells[3], fix: cells[4] });
  }
  return rows;
}

describe('CLOUD_CODES', () => {
  it('has a distinct code string per entry', () => {
    const codes = Object.values(CLOUD_CODES).map((entry) => entry.code);
    expect(new Set(codes).size).toBe(codes.length);
    for (const code of codes) expect(code).toMatch(/^LAI-CLD-\d{3}$/);
  });

  // The registry lives outside apps/cloud; a copy of the app taken out of the monorepo skips the comparison.
  (existsSync(REGISTRY) ? it : it.skip)('matches docs/specs/doctor-codes.md word for word', () => {
    const rows = registryRows();
    for (const entry of Object.values(CLOUD_CODES)) {
      const row = rows.get(entry.code);
      expect(row).toBeDefined();
      expect({ code: entry.code, severity: entry.severity, title: entry.title, fix: entry.fix }).toEqual({
        code: entry.code,
        severity: row?.severity,
        title: row?.meaning,
        fix: row?.fix,
      });
    }
  });
});
