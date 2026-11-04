// =============================================================================
// The identity slice's notifications, declared by the app
// =============================================================================
//
// Identity raises four notices through its `IDENTITY_NOTIFIER` port
// (`./identity-notifier.adapter.ts`); the events and which templates and
// browser renderers they use are declared here and registered by
// `./notification.manifest.ts`. An event key is persisted (in preferences and
// delivery rows): never rename one, add a new key.
//
// `user.welcome` has no browser renderer on purpose: it fires while the user
// is looking at the page that welcomes them, a toast with no reader.
// `allowlist.invitation` and `org.invitation` are email only: their recipient
// may have no account, so no in-app channel can reach them.
// =============================================================================

import { roleChangedBrowserTemplate, type NotificationRegistration } from '@marinoscar/platform-api/notifications';

export const IDENTITY_NOTIFICATIONS: readonly NotificationRegistration[] = [
  {
    event: {
      key: 'user.welcome',
      label: 'Welcome',
      description: 'Sent once, the first time you sign in to this application.',
      channels: ['email'],
      defaultEnabled: true,
    },
    emailTemplate: 'user-welcome',
  },
  {
    event: {
      key: 'allowlist.invitation',
      label: 'Invitation to join',
      description: 'Sent when an administrator adds your email address to the allowlist, inviting you to sign in.',
      channels: ['email'],
      defaultEnabled: true,
    },
    emailTemplate: 'allowlist-invitation',
  },
  {
    event: {
      key: 'security.role_changed',
      label: 'Your roles changed',
      description: 'Sent when an administrator changes the roles assigned to your account, which changes what you can access.',
      channels: ['email', 'browser'],
      defaultEnabled: true,
      // A privilege change the user never hears about is the failure this flag
      // exists for: not silenceable.
      mandatory: true,
    },
    emailTemplate: 'role-changed',
    browserTemplate: roleChangedBrowserTemplate,
  },
  {
    event: {
      key: 'org.invitation',
      label: 'Invitation to an organization',
      description: 'Sent when an organization administrator invites your email address to join their organization.',
      channels: ['email'],
      defaultEnabled: true,
    },
    emailTemplate: 'org-invitation',
  },
];
