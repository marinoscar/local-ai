import { AuditService, likeLiteral } from './audit.service';
import { AuditQuerySchema } from './audit.schemas';

describe('likeLiteral', () => {
  it('escapes the LIKE wildcards and the escape character, and nothing else', () => {
    expect(likeLiteral('localai.node.mode_changed')).toBe('localai.node.mode\\_changed');
    expect(likeLiteral('a%b_c\\d')).toBe('a\\%b\\_c\\\\d');
    expect(likeLiteral('localai.command.')).toBe('localai.command.');
  });
});

describe('AuditService.list', () => {
  const now = new Date('2026-11-04T12:00:00.000Z');
  const prisma = {
    auditEvent: { findMany: jest.fn(), count: jest.fn() },
    user: { findMany: jest.fn() },
    localaiNode: { findUnique: jest.fn(), findMany: jest.fn() },
  };
  const service = new AuditService(prisma as never);

  beforeEach(() => {
    jest.resetAllMocks();
    prisma.auditEvent.findMany.mockResolvedValue([]);
    prisma.auditEvent.count.mockResolvedValue(0);
    prisma.user.findMany.mockResolvedValue([]);
  });

  it('escapes the action prefix before it reaches LIKE', async () => {
    await service.list(AuditQuerySchema.parse({ action: 'localai.node.mode_changed' }), now);
    expect(prisma.auditEvent.findMany.mock.calls[0][0].where.action).toEqual({ startsWith: 'localai.node.mode\\_changed' });
  });

  it('defaults from to 30 days before now and leaves to open', async () => {
    await service.list(AuditQuerySchema.parse({}), now);
    expect(prisma.auditEvent.findMany.mock.calls[0][0].where.createdAt).toEqual({ gte: new Date('2026-10-05T12:00:00.000Z') });
  });

  it('answers an empty page with one page in total, not zero', async () => {
    expect(await service.list(AuditQuerySchema.parse({ page: '2' }), now)).toEqual({ items: [], page: 2, pageSize: 50, total: 0, totalPages: 1 });
  });

  it('reads no user rows when no row has an actor', async () => {
    prisma.auditEvent.findMany.mockResolvedValue([{ id: '1', actorUserId: null, action: 'localai.x.y', targetType: 't', targetId: 'i', meta: null, createdAt: now }]);
    const page = await service.list(AuditQuerySchema.parse({}), now);
    expect(prisma.user.findMany).not.toHaveBeenCalled();
    expect(page.items[0]).toMatchObject({ actor: { kind: 'system', label: 'system' }, summary: 'localai.x.y', link: null });
  });
});

describe('AuditQuerySchema', () => {
  it('reads a calendar day as the whole UTC day on both ends', () => {
    const parsed = AuditQuerySchema.parse({ from: '2026-11-01', to: '2026-11-01' });
    expect(parsed.from?.toISOString()).toBe('2026-11-01T00:00:00.000Z');
    expect(parsed.to?.toISOString()).toBe('2026-11-01T23:59:59.999Z');
  });

  it('accepts a full ISO instant with an offset', () => {
    expect(AuditQuerySchema.parse({ from: '2026-11-01T10:00:00+02:00' }).from?.toISOString()).toBe('2026-11-01T08:00:00.000Z');
  });

  it.each([{ from: 'now' }, { from: '2026-13-01' }, { to: '11/02/2026' }, { page: 0 }, { page: 1.5 }, { action: 'users.' }, { action: 'localai.A' }, { nodeId: 'ai n3' }, { extra: 1 }, { from: '2026-11-03', to: '2026-11-01' }])(
    'refuses %j',
    (query) => {
      expect(AuditQuerySchema.safeParse(query).success).toBe(false);
    },
  );
});
