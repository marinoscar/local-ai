// The generated copy of the doctor registry cannot drift: it must equal what scripts/gen-doctor-fixes.mjs writes from
// docs/specs/doctor-codes.md. Run `node apps/cloud/scripts/gen-doctor-fixes.mjs` after editing the registry.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import { render, parseRegistry } from '../../../../scripts/gen-doctor-fixes.mjs';
import { plainError, registryRow } from './doctorFix';
import { DOCTOR_FIXES } from './doctorFixes.generated';

const ROOT = join(__dirname, '../../../../../..');

describe('doctorFixes.generated.ts', () => {
  it('equals what the generator writes from the registry', () => {
    const rows = parseRegistry(readFileSync(join(ROOT, 'docs/specs/doctor-codes.md'), 'utf8'));
    expect(rows.length).toBeGreaterThan(80);
    expect(readFileSync(join(__dirname, 'doctorFixes.generated.ts'), 'utf8')).toBe(render(rows));
  });

  it('has a meaning and a fix for every code, and only codes of the areas a node page can show', () => {
    for (const [code, row] of Object.entries(DOCTOR_FIXES)) {
      expect(code).toMatch(/^LAI-(CLD|SYN|ARK|LLM|CAT)-\d{3}$/);
      expect(row.meaning).not.toBe('');
      expect(row.fix).not.toBe('');
    }
  });
});

describe('plainError', () => {
  it('looks a code up, ignores anything that is not a code, and never throws on odd input', () => {
    expect(registryRow('LAI-SYN-031')?.fix).toContain('internet connection');
    expect(registryRow('not a code')).toBeNull();
    expect(registryRow('LAI-ZZZ-999')).toBeNull();
    expect(plainError({ code: 'bogus', message: 'plain message' })).toEqual({ code: null, meaning: null, fix: 'plain message' });
    expect(plainError(undefined).fix).not.toBe('');
  });
});
