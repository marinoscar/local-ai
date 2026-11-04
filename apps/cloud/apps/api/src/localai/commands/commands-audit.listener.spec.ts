import { CommandsAuditListener } from './commands-audit.listener';
import { COMMAND_SETTLED_CHANNEL } from './command-events';

const COMMAND = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb1';
const USER = 'user-alpha';

interface Row {
  id: string;
  type: string;
  status: string;
  error: unknown;
  requestedById: string | null;
  node: { nodeId: string; displayName: string };
}

const row = (overrides: Partial<Row> = {}): Row => ({
  id: COMMAND,
  type: 'localai.model.load',
  status: 'succeeded',
  error: null,
  requestedById: USER,
  node: { nodeId: 'ai-n2', displayName: 'Node ai-n2' },
  ...overrides,
});

function build(found: Row | null, existingAudit = false) {
  const handlers: Array<(event: unknown) => void> = [];
  const unsubscribe = jest.fn();
  const bus = { subscribe: jest.fn((_channel: string, handler: (event: unknown) => void) => (handlers.push(handler), unsubscribe)) };
  const prisma = {
    localaiCommand: { findUnique: jest.fn(async () => found) },
    auditEvent: { findFirst: jest.fn(async () => (existingAudit ? { id: 'audit-1' } : null)) },
  };
  const audit = { record: jest.fn().mockResolvedValue(undefined) };
  const notifier = { commandFailed: jest.fn().mockResolvedValue(undefined) };
  const listener = new CommandsAuditListener(prisma as never, bus as never, audit, notifier);
  return { listener, bus, handlers, unsubscribe, prisma, audit, notifier };
}

describe('CommandsAuditListener', () => {
  it('subscribes to localai.command.settled on init and unsubscribes on destroy', () => {
    const { listener, bus, unsubscribe } = build(row());
    listener.onModuleInit();
    expect(bus.subscribe).toHaveBeenCalledWith(COMMAND_SETTLED_CHANNEL, expect.any(Function));
    listener.onModuleDestroy();
    expect(unsubscribe).toHaveBeenCalledTimes(1);
  });

  it('audits a succeeded command once with {nodeId, type, status} and sends no notification', async () => {
    const { listener, audit, notifier } = build(row());
    await listener.handleSettled({ nodeId: 'node-row', commandId: COMMAND, status: 'succeeded' });
    expect(audit.record).toHaveBeenCalledTimes(1);
    expect(audit.record).toHaveBeenCalledWith({
      action: 'localai.command.settled',
      actorUserId: USER,
      targetType: 'localai_command',
      targetId: COMMAND,
      meta: { nodeId: 'ai-n2', type: 'localai.model.load', status: 'succeeded' },
    });
    expect(notifier.commandFailed).not.toHaveBeenCalled();
  });

  it('never selects the result column, so a logs.tail result cannot reach the audit row or the message', async () => {
    const { listener, prisma } = build(row({ type: 'localai.logs.tail' }));
    await listener.handleSettled({ nodeId: 'n', commandId: COMMAND, status: 'succeeded' });
    const select = (prisma.localaiCommand.findUnique.mock.calls[0] as unknown as [{ select: Record<string, unknown> }])[0].select;
    expect(select).not.toHaveProperty('result');
    expect(select).not.toHaveProperty('params');
  });

  it('audits a failed command with the error code and notifies the requester once with the plain-language fix', async () => {
    const { listener, audit, notifier } = build(row({ status: 'failed', error: { code: 'LAI-CAT-005', message: 'Model does not fit in free VRAM' } }));
    await listener.handleSettled({ nodeId: 'n', commandId: COMMAND, status: 'failed' });
    expect(audit.record).toHaveBeenCalledWith(expect.objectContaining({ meta: { nodeId: 'ai-n2', type: 'localai.model.load', status: 'failed', errorCode: 'LAI-CAT-005' } }));
    expect(notifier.commandFailed).toHaveBeenCalledTimes(1);
    expect(notifier.commandFailed).toHaveBeenCalledWith({
      recipientUserId: USER,
      commandId: COMMAND,
      nodeId: 'ai-n2',
      nodeName: 'Node ai-n2',
      type: 'model.load',
      status: 'failed',
      errorCode: 'LAI-CAT-005',
      fix: 'Choose a smaller model. `aictl models --loadable` lists the ones that fit (LAI-CAT-005)',
      message: 'model.load on Node ai-n2 failed: Choose a smaller model. `aictl models --loadable` lists the ones that fit (LAI-CAT-005)',
    });
  });

  it('notifies for an expired command too (nobody claimed it in time)', async () => {
    const { listener, notifier } = build(row({ status: 'expired', error: { code: 'LAI-CLD-030', message: 'The node did not pick this command up in time' } }));
    await listener.handleSettled({ nodeId: 'n', commandId: COMMAND, status: 'expired' });
    expect(notifier.commandFailed).toHaveBeenCalledWith(expect.objectContaining({ status: 'expired', errorCode: 'LAI-CLD-030' }));
  });

  it('audits but does not notify when nobody requested the command (a system or catalog job with no actor)', async () => {
    const { listener, audit, notifier } = build(row({ status: 'failed', requestedById: null, error: { code: 'LAI-CLD-012', message: 'x' } }));
    await listener.handleSettled({ nodeId: 'n', commandId: COMMAND, status: 'failed' });
    expect(audit.record).toHaveBeenCalledWith(expect.objectContaining({ actorUserId: null }));
    expect(notifier.commandFailed).not.toHaveBeenCalled();
  });

  it('is idempotent: a second delivery for a command already audited writes and sends nothing', async () => {
    const { listener, audit, notifier } = build(row({ status: 'failed', error: { code: 'LAI-CLD-012', message: 'x' } }), true);
    await listener.handleSettled({ nodeId: 'n', commandId: COMMAND, status: 'failed' });
    expect(audit.record).not.toHaveBeenCalled();
    expect(notifier.commandFailed).not.toHaveBeenCalled();
  });

  it('ignores a command that is gone or not settled, and a malformed event', async () => {
    for (const found of [null, row({ status: 'running' }), row({ status: 'cancelled' })]) {
      const { listener, audit, notifier } = build(found);
      await listener.handleSettled({ nodeId: 'n', commandId: COMMAND, status: 'failed' });
      expect(audit.record).not.toHaveBeenCalled();
      expect(notifier.commandFailed).not.toHaveBeenCalled();
    }
    const { listener, prisma } = build(row());
    await listener.handleSettled(undefined as never);
    expect(prisma.localaiCommand.findUnique).not.toHaveBeenCalled();
  });

  it('a failing audit sink does not stop the notification, and a failing notifier or lookup never throws', async () => {
    const a = build(row({ status: 'failed', error: { code: 'LAI-CLD-012', message: 'x' } }));
    a.audit.record.mockRejectedValue(new Error('audit down'));
    await expect(a.listener.handleSettled({ nodeId: 'n', commandId: COMMAND, status: 'failed' })).resolves.toBeUndefined();
    expect(a.notifier.commandFailed).toHaveBeenCalledTimes(1);

    const b = build(row({ status: 'failed', error: { code: 'LAI-CLD-012', message: 'x' } }));
    b.notifier.commandFailed.mockRejectedValue(new Error('push down'));
    await expect(b.listener.handleSettled({ nodeId: 'n', commandId: COMMAND, status: 'failed' })).resolves.toBeUndefined();
    expect(b.audit.record).toHaveBeenCalledTimes(1);

    const c = build(row());
    c.prisma.localaiCommand.findUnique.mockRejectedValue(new Error('db down'));
    await expect(c.listener.handleSettled({ nodeId: 'n', commandId: COMMAND, status: 'succeeded' })).resolves.toBeUndefined();
  });

  it('the subscribed handler runs handleSettled', async () => {
    const { listener, handlers, audit } = build(row());
    listener.onModuleInit();
    handlers[0]({ nodeId: 'n', commandId: COMMAND, status: 'succeeded' });
    await new Promise((resolve) => setImmediate(resolve));
    expect(audit.record).toHaveBeenCalledTimes(1);
  });
});
