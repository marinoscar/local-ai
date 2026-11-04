// =============================================================================
// IDENTITY_NOTIFIER -> the notification dispatcher
// =============================================================================
//
// Identity raises its four notices through this port, so the identity package
// never imports the notifications slice. Each method is one `notify` /
// `notifyAddress` call with the event key and the payload identity always
// sends. Called after the triggering write committed and outside any
// transaction; `notify` never rejects (delivery failures are recorded by the
// dispatcher). Replaces the starter's logging notifier once the slice is on.
// =============================================================================

import { Injectable } from '@nestjs/common';
import type {
  AllowlistInvitationNotice,
  IdentityNotifier,
  OrgInvitationNotice,
  RoleChangedNotice,
  UserWelcomeNotice,
} from '@marinoscar/platform-api/identity';
import { NotificationsService } from '@marinoscar/platform-api/notifications';

@Injectable()
export class NotificationsIdentityNotifier implements IdentityNotifier {
  constructor(private readonly notifications: NotificationsService) {}

  async roleChanged(userId: string, notice: RoleChangedNotice): Promise<void> {
    await this.notifications.notify('security.role_changed', userId, notice);
  }

  async userWelcomed(userId: string, notice: UserWelcomeNotice): Promise<void> {
    await this.notifications.notify('user.welcome', userId, notice);
  }

  async allowlistInvitation(email: string, notice: AllowlistInvitationNotice): Promise<void> {
    await this.notifications.notifyAddress('allowlist.invitation', email, notice);
  }

  async orgInvitation(email: string, notice: OrgInvitationNotice): Promise<void> {
    await this.notifications.notifyAddress('org.invitation', email, notice);
  }
}
