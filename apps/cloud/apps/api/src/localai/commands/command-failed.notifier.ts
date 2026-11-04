// The real `LOCALAI_COMMAND_NOTIFIER` (`command-notifier.ts`): "{type} on {node} failed: {fix}" as a browser notification to
// the person who sent the command, and nobody else. The settled-command listener calls it once per failed or expired
// command that has a requester, after the settling write committed and outside any transaction; `notify` never rejects.
// `@Optional()` like the other Local AI notifiers: without the notifications slice it only logs.
import { Injectable, Logger, Optional } from '@nestjs/common';
import { NotificationsService } from '@marinoscar/platform-api/notifications';

import { NOTIFICATION_COMMAND_FAILED } from '../localai.notifications';
import type { LocalaiCommandFailedEvent, LocalaiCommandNotifier } from './command-notifier';

@Injectable()
export class NotificationsCommandNotifier implements LocalaiCommandNotifier {
  private readonly logger = new Logger(NotificationsCommandNotifier.name);

  constructor(@Optional() private readonly notifications?: NotificationsService) {}

  async commandFailed(event: LocalaiCommandFailedEvent): Promise<void> {
    if (!this.notifications) {
      this.logger.debug(`${NOTIFICATION_COMMAND_FAILED}: command_id=${event.commandId} (notifications slice not mounted)`);
      return;
    }
    const { recipientUserId, ...data } = event;
    await this.notifications.notify(NOTIFICATION_COMMAND_FAILED, recipientUserId, data, { channels: ['browser'] });
  }
}
