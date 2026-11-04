import { NotificationsCommandNotifier } from './command-failed.notifier';
import type { LocalaiCommandFailedEvent } from './command-notifier';

const event: LocalaiCommandFailedEvent = {
  recipientUserId: 'user-7',
  commandId: 'c-1',
  nodeId: 'ai-n2',
  nodeName: 'Node Two',
  type: 'model.load',
  status: 'failed',
  errorCode: null,
  fix: 'Download it first.',
  message: 'model.load on Node Two failed: Download it first.',
};

describe('NotificationsCommandNotifier', () => {
  it('tells the requester, over the browser channel, and nobody else', async () => {
    const notify = jest.fn(async () => undefined);
    await new NotificationsCommandNotifier({ notify } as never).commandFailed(event);
    expect(notify).toHaveBeenCalledTimes(1);
    const [key, userId, data, options] = notify.mock.calls[0] as unknown as [string, string, Record<string, unknown>, unknown];
    expect(key).toBe('localai.command_failed');
    expect(userId).toBe('user-7');
    expect(options).toEqual({ channels: ['browser'] });
    expect(data).toMatchObject({ commandId: 'c-1', nodeId: 'ai-n2', type: 'model.load', fix: 'Download it first.' });
    expect(data).not.toHaveProperty('recipientUserId');
  });

  it('without the notifications slice it does nothing and does not throw', async () => {
    await expect(new NotificationsCommandNotifier(undefined).commandFailed(event)).resolves.toBeUndefined();
  });
});
