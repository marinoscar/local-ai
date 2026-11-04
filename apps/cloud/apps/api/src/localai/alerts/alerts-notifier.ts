// The alert engine's notifications, sent through the platform's notifications slice (docs/specs/cloud-app.md section 4.9).
// Called by `AlertsService` AFTER the alert write committed and outside any transaction; `notifyPermissionHolders` never
// rejects (a delivery failure is a log line and a delivery row, never an error here).
//
// CHANNELS. Browser for every severity; email only for `critical` (`info` and `warning` never mail). The event declares
// both channels; narrowing to `['browser']` per dispatch can only remove a channel, and a user's preferences can still mute
// either. Audience: the holders of `lab:operate`.
//
// The dispatcher is `@Optional()`: an app that does not mount the notifications slice still boots, and then the alerts are
// simply not announced (they are still raised, listed on the Alerts page and counted on the Lab overview).
import { Injectable, Logger, Optional } from '@nestjs/common';
import { NotificationsService } from '@marinoscar/platform-api/notifications';

import { LOCALAI_PERMISSIONS } from '../localai.permissions';
import { NOTIFICATION_ALERT_CLEARED, NOTIFICATION_ALERT_RAISED } from '../localai.notifications';
import type { AlertNotice, AlertNotifier } from './alert-ports';

@Injectable()
export class NotificationsAlertNotifier implements AlertNotifier {
  private readonly logger = new Logger(NotificationsAlertNotifier.name);

  constructor(@Optional() private readonly notifications?: NotificationsService) {}

  async raised(notice: AlertNotice): Promise<void> {
    if (!this.notifications) {
      this.logger.debug(`${NOTIFICATION_ALERT_RAISED}: alert_id=${notice.alertId} (notifications slice not mounted)`);
      return;
    }
    const channels = notice.severity === 'critical' ? (['browser', 'email'] as const) : (['browser'] as const);
    await this.notifications.notifyPermissionHolders(NOTIFICATION_ALERT_RAISED, LOCALAI_PERMISSIONS.LAB_OPERATE.id, notice, { channels });
  }

  async cleared(notice: AlertNotice): Promise<void> {
    if (!this.notifications) return;
    await this.notifications.notifyPermissionHolders(NOTIFICATION_ALERT_CLEARED, LOCALAI_PERMISSIONS.LAB_OPERATE.id, notice, { channels: ['browser'] });
  }
}
