// Proves `fixtures/localai/audit-page.json` is exactly what the real audit service answers for the rows in
// `support/audit-fixture.inputs.ts`: the web tests render the JSON, so they cannot drift from the server. Regenerate
// after an intended change with `UPDATE_AUDIT_FIXTURE=1 npm test --workspace=api -- audit-fixture`.
import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

import { AuditService } from '../../src/localai/audit/audit.service';
import { AuditQuerySchema } from '../../src/localai/audit/audit.schemas';
import { AUDIT_FIXTURE_NOW, AUDIT_FIXTURE_TOTAL, AUDIT_FIXTURE_USERS, auditFixtureEvents } from './support/audit-fixture.inputs';

const FILE = join(__dirname, '../fixtures/localai/audit-page.json');

describe('the audit page fixture', () => {
  it('equals the service answer for the fixture rows', async () => {
    const prisma = {
      auditEvent: { findMany: jest.fn().mockResolvedValue(auditFixtureEvents()), count: jest.fn().mockResolvedValue(AUDIT_FIXTURE_TOTAL) },
      user: { findMany: jest.fn().mockResolvedValue(AUDIT_FIXTURE_USERS) },
      localaiNode: { findUnique: jest.fn(), findMany: jest.fn().mockResolvedValue([]) },
    };
    const page = await new AuditService(prisma as never).list(AuditQuerySchema.parse({}), AUDIT_FIXTURE_NOW);
    if (process.env.UPDATE_AUDIT_FIXTURE === '1') writeFileSync(FILE, `${JSON.stringify(page, null, 2)}\n`);
    expect(page).toEqual(JSON.parse(readFileSync(FILE, 'utf8')));
  });

  it('holds no secret-shaped text, and shows the unknown action as its name', () => {
    const text = readFileSync(FILE, 'utf8');
    expect(text).not.toMatch(/nod_[A-Za-z0-9_-]{20,}/);
    expect(text).not.toMatch(/\b100\.\d+\.\d+\.\d+\b/);
    expect(text).not.toContain('Bearer');
    const rows = (JSON.parse(text) as { items: Array<{ action: string; summary: string }> }).items;
    expect(rows.find((row) => row.action === 'localai.future.thing')?.summary).toBe('localai.future.thing');
  });
});
