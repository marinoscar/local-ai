// The dispatcher side: which event, which audience, which channels, for each severity. Browser for every severity,
// email only for critical; `info` and `warning` never mail.
import { NotificationsAlertNotifier } from './alerts-notifier';
import { NotificationsNodeNotifier } from '../node-link/node-enrolled.notifier';
import type { AlertNotice } from './alert-ports';

const notice = (severity: AlertNotice['severity']): AlertNotice => ({ alertId: 'a-1', node: 'ai-n2', kind: 'ark.drive_stale', code: 'LAI-CLD-022', severity, title: 'Ark drive not synced for 95 days', fix: 'Plug it in.' });

function fake(holders: string[] = ['user-1', 'user-2']) {
  const notify = jest.fn(async () => undefined);
  const holdersOf = jest.fn(async () => holders);
  return { notify, holdersOf, service: { notify }, audience: { holdersOf } };
}

describe('NotificationsAlertNotifier', () => {
  it.each([
    ['info', ['browser']],
    ['warning', ['browser']],
    ['critical', ['browser', 'email']],
  ] as const)('a %s alert goes to lab:operate over %j', async (severity, channels) => {
    const { notify, holdersOf, service, audience } = fake();
    await new NotificationsAlertNotifier(audience as never, service as never).raised(notice(severity));
    expect(holdersOf).toHaveBeenCalledWith('lab:operate');
    expect(notify).toHaveBeenCalledTimes(2);
    expect(notify).toHaveBeenNthCalledWith(1, 'localai.alert_raised', 'user-1', notice(severity), { channels });
    expect(notify).toHaveBeenNthCalledWith(2, 'localai.alert_raised', 'user-2', notice(severity), { channels });
  });

  it('a resolved alert is browser only', async () => {
    const { notify, service, audience } = fake(['user-1']);
    await new NotificationsAlertNotifier(audience as never, service as never).cleared(notice('critical'));
    expect(notify).toHaveBeenCalledWith('localai.alert_cleared', 'user-1', notice('critical'), { channels: ['browser'] });
  });

  it('without the notifications slice it does nothing and does not throw', async () => {
    const { holdersOf, audience } = fake();
    const sut = new NotificationsAlertNotifier(audience as never, undefined);
    await expect(sut.raised(notice('critical'))).resolves.toBeUndefined();
    await expect(sut.cleared(notice('critical'))).resolves.toBeUndefined();
    expect(holdersOf).not.toHaveBeenCalled();
  });

  it('nobody holds the permission: nothing is sent', async () => {
    const { notify, service, audience } = fake([]);
    await new NotificationsAlertNotifier(audience as never, service as never).raised(notice('critical'));
    expect(notify).not.toHaveBeenCalled();
  });
});

describe('NotificationsNodeNotifier', () => {
  it('"{node} joined the lab" goes to lab:admin over browser and email', async () => {
    const { notify, holdersOf, service, audience } = fake(['admin-1']);
    await new NotificationsNodeNotifier(audience as never, service as never).nodeEnrolled({ nodeId: 'ai-n4', displayName: 'Node Four', workerNodeId: 'w-1' });
    expect(holdersOf).toHaveBeenCalledWith('lab:admin');
    expect(notify).toHaveBeenCalledWith('localai.node_enrolled', 'admin-1', { node: 'ai-n4', displayName: 'Node Four' }, { channels: ['browser', 'email'] });
  });

  it('without the notifications slice it only logs', async () => {
    await expect(new NotificationsNodeNotifier(fake().audience as never, undefined).nodeEnrolled({ nodeId: 'ai-n4', displayName: 'x', workerNodeId: 'w' })).resolves.toBeUndefined();
  });
});
