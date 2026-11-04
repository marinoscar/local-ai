// The dispatcher side: which event, which audience, which channels, for each severity. Browser for every severity,
// email only for critical; `info` and `warning` never mail.
import { NotificationsAlertNotifier } from './alerts-notifier';
import { NotificationsNodeNotifier } from '../node-link/node-enrolled.notifier';
import type { AlertNotice } from './alert-ports';

const notice = (severity: AlertNotice['severity']): AlertNotice => ({ alertId: 'a-1', node: 'ai-n2', kind: 'ark.drive_stale', code: 'LAI-CLD-022', severity, title: 'Ark drive not synced for 95 days', fix: 'Plug it in.' });

function fake() {
  const notifyPermissionHolders = jest.fn(async () => undefined);
  return { notifyPermissionHolders, service: { notifyPermissionHolders } };
}

describe('NotificationsAlertNotifier', () => {
  it.each([
    ['info', ['browser']],
    ['warning', ['browser']],
    ['critical', ['browser', 'email']],
  ] as const)('a %s alert goes to lab:operate over %j', async (severity, channels) => {
    const { notifyPermissionHolders, service } = fake();
    await new NotificationsAlertNotifier(service as never).raised(notice(severity));
    expect(notifyPermissionHolders).toHaveBeenCalledTimes(1);
    expect(notifyPermissionHolders).toHaveBeenCalledWith('localai.alert_raised', 'lab:operate', notice(severity), { channels });
  });

  it('a resolved alert is browser only', async () => {
    const { notifyPermissionHolders, service } = fake();
    await new NotificationsAlertNotifier(service as never).cleared(notice('critical'));
    expect(notifyPermissionHolders).toHaveBeenCalledWith('localai.alert_cleared', 'lab:operate', notice('critical'), { channels: ['browser'] });
  });

  it('without the notifications slice it does nothing and does not throw', async () => {
    const sut = new NotificationsAlertNotifier(undefined);
    await expect(sut.raised(notice('critical'))).resolves.toBeUndefined();
    await expect(sut.cleared(notice('critical'))).resolves.toBeUndefined();
  });
});

describe('NotificationsNodeNotifier', () => {
  it('"{node} joined the lab" goes to lab:admin over browser and email', async () => {
    const { notifyPermissionHolders, service } = fake();
    await new NotificationsNodeNotifier(service as never).nodeEnrolled({ nodeId: 'ai-n4', displayName: 'Node Four', workerNodeId: 'w-1' });
    expect(notifyPermissionHolders).toHaveBeenCalledWith('localai.node_enrolled', 'lab:admin', { node: 'ai-n4', displayName: 'Node Four' }, { channels: ['browser', 'email'] });
  });

  it('without the notifications slice it only logs', async () => {
    await expect(new NotificationsNodeNotifier(undefined).nodeEnrolled({ nodeId: 'ai-n4', displayName: 'x', workerNodeId: 'w' })).resolves.toBeUndefined();
  });
});
