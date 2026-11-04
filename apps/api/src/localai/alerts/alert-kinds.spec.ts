import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import { CODE_FIXES, EVALUATOR_KINDS, EXTERNAL_KINDS, GENERIC_FIX, KIND_CODES, isEvaluatorOwnedKind, isExternalKind, plainFix } from './alert-kinds';

// The registry is the single source of truth for codes and fixes (AGENTS.md rule 5); this keeps the copy in step.
const registry = readFileSync(join(__dirname, '../../../../../../../docs/specs/doctor-codes.md'), 'utf8');

function registryRow(code: string): { severity: string; fix: string } {
  const line = registry.split('\n').find((candidate) => candidate.startsWith(`| ${code} |`));
  if (!line) throw new Error(`${code} is not in docs/specs/doctor-codes.md`);
  const cells = line.split('|').map((cell) => cell.trim());
  return { severity: cells[2]!, fix: cells[4]! };
}

const norm = (text: string) => text.replace(/\.$/, '').trim();

describe('alert kinds and codes', () => {
  it('every kind has a LAI-CLD code in the registry, and the fix text is the registry\'s, word for word', () => {
    for (const kind of [...EVALUATOR_KINDS, ...EXTERNAL_KINDS]) {
      const code = KIND_CODES[kind];
      expect(registryRow(code).fix).toBeTruthy();
      expect(norm(CODE_FIXES[code]!)).toBe(norm(registryRow(code).fix));
    }
  });

  it('the codes run LAI-CLD-020 to 029 in the order of the spec table', () => {
    expect([...EVALUATOR_KINDS, ...EXTERNAL_KINDS].map((kind) => KIND_CODES[kind]).sort()).toEqual(Array.from({ length: 10 }, (_, index) => `LAI-CLD-0${20 + index}`));
  });

  it('plainFix never returns an empty string', () => {
    expect(plainFix('LAI-CLD-022')).toContain('AI-ARK drive');
    expect(plainFix('LAI-HW-020')).toBe(GENERIC_FIX);
    expect(GENERIC_FIX.length).toBeGreaterThan(20);
  });

  it('ownership: the evaluator owns its kinds and every node-reported one, never an external one', () => {
    expect(isEvaluatorOwnedKind('node.stale')).toBe(true);
    expect(isEvaluatorOwnedKind('node.alerts.LAI-HW-020')).toBe(true);
    expect(isEvaluatorOwnedKind('endpoint.down')).toBe(false);
    expect(isExternalKind('endpoint.down')).toBe(true);
    expect(isExternalKind('node.stale')).toBe(false);
  });
});
