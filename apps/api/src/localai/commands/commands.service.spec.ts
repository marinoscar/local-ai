import { Prisma } from '@prisma/client';

import { isCommandDedupConflict } from './commands.service';

const p2002 = (meta: Record<string, unknown>) => new Prisma.PrismaClientKnownRequestError('Unique constraint failed', { code: 'P2002', clientVersion: 'test', meta });

describe('isCommandDedupConflict', () => {
  it('recognises the violation of the active-dedup index in every shape Prisma reports it', () => {
    expect(isCommandDedupConflict(p2002({ driverAdapterError: { cause: { constraint: { fields: ['dedup_key'] } } } }))).toBe(true);
    expect(isCommandDedupConflict(p2002({ driverAdapterError: { cause: { constraint: { index: 'localai_commands_active_dedup' } } } }))).toBe(true);
    expect(isCommandDedupConflict(p2002({ driverAdapterError: { cause: { originalMessage: 'duplicate key value violates unique constraint "localai_commands_active_dedup"' } } }))).toBe(true);
    expect(isCommandDedupConflict(p2002({ target: 'localai_commands_active_dedup' }))).toBe(true);
    expect(isCommandDedupConflict(p2002({ target: ['node_id', 'dedup_key'] }))).toBe(true);
  });

  it('does not take another unique violation, another error code or a plain error for a dedup conflict', () => {
    expect(isCommandDedupConflict(p2002({ target: ['id'] }))).toBe(false);
    expect(isCommandDedupConflict(p2002({}))).toBe(false);
    expect(isCommandDedupConflict(new Prisma.PrismaClientKnownRequestError('x', { code: 'P2003', clientVersion: 'test', meta: { target: 'dedup_key' } }))).toBe(false);
    expect(isCommandDedupConflict(new Error('dedup_key'))).toBe(false);
    expect(isCommandDedupConflict(null)).toBe(false);
  });
});
