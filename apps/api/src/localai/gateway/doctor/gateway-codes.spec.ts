// The gateway's doctor codes equal the registry rows of docs/specs/doctor-codes.md word for word.
import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

import { GATEWAY_CODES } from './gateway-codes';

// apps/cloud/apps/api/src/localai/gateway/doctor -> the repository root.
const REGISTRY = join(__dirname, '..', '..', '..', '..', '..', '..', '..', '..', 'docs', 'specs', 'doctor-codes.md');

function registryRows(): Map<string, { severity: string; meaning: string; fix: string }> {
  const rows = new Map<string, { severity: string; meaning: string; fix: string }>();
  for (const line of readFileSync(REGISTRY, 'utf8').split('\n')) {
    const cells = line.split('|').map((cell) => cell.trim());
    if (/^LAI-[A-Z]+-\d{3}$/.test(cells[1] ?? '')) rows.set(cells[1], { severity: cells[2], meaning: cells[3], fix: cells[4] });
  }
  return rows;
}

describe('GATEWAY_CODES', () => {
  it('has a distinct LAI-GW code per entry', () => {
    const codes = Object.values(GATEWAY_CODES).map((entry) => entry.code);
    expect(new Set(codes).size).toBe(codes.length);
    for (const code of codes) expect(code).toMatch(/^LAI-GW-\d{3}$/);
  });

  (existsSync(REGISTRY) ? it : it.skip)('matches docs/specs/doctor-codes.md word for word', () => {
    const rows = registryRows();
    for (const entry of Object.values(GATEWAY_CODES)) {
      const row = rows.get(entry.code);
      expect(row).toBeDefined();
      expect({ code: entry.code, severity: entry.severity, title: entry.title, fix: entry.fix }).toEqual({ code: entry.code, severity: row?.severity, title: row?.meaning, fix: row?.fix });
    }
  });
});
