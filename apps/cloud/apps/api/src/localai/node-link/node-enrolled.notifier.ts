// The real `LOCALAI_NODE_NOTIFIER` (`node-link.notifier.ts`): "{node} joined the lab" to the holders of `lab:admin`,
// browser and email. The binding guard calls it once per node, after the enrolling write committed and outside any
// transaction; the dispatcher never rejects. `@Optional()` like the alert notifier: without the notifications slice it
// only leaves a log line.
import { Injectable, Logger, Optional } from '@nestjs/common';
import { NotificationsService } from '@marinoscar/platform-api/notifications';

import { NOTIFICATION_NODE_ENROLLED } from '../localai.notifications';
import { LOCALAI_PERMISSIONS } from '../localai.permissions';
import type { LocalaiNodeEnrolledEvent, LocalaiNodeNotifier } from './node-link.notifier';

@Injectable()
export class NotificationsNodeNotifier implements LocalaiNodeNotifier {
  private readonly logger = new Logger(NotificationsNodeNotifier.name);

  constructor(@Optional() private readonly notifications?: NotificationsService) {}

  async nodeEnrolled(event: LocalaiNodeEnrolledEvent): Promise<void> {
    if (!this.notifications) {
      this.logger.log(`${NOTIFICATION_NODE_ENROLLED}: node_id=${event.nodeId} (notifications slice not mounted)`);
      return;
    }
    await this.notifications.notifyPermissionHolders(
      NOTIFICATION_NODE_ENROLLED,
      LOCALAI_PERMISSIONS.LAB_ADMIN.id,
      { node: event.nodeId, displayName: event.displayName },
      { channels: ['browser', 'email'] },
    );
  }
}
