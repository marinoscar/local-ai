import type { EmailTemplate, EmailTemplateEntry } from '@marinoscar/platform-api/email';

import { orgInvitationEmail, type OrgInvitationEmailData } from './org-invitation.email';

// =============================================================================
// Email templates this app owns the words of
// =============================================================================
//
// `org-invitation` (the identity slice's organization invitation: the platform
// ships the other nine). A template
// name is a stable id: notification events map to it. The typed data map below
// is what makes `renderEmailTemplate('org-invitation', data)` check its data.
// Registered by the email slice (`../email.slice.ts`) at import time, never
// from `onModuleInit`.
// =============================================================================

declare module '@marinoscar/platform-api/email' {
  interface EmailTemplateDataMap {
    'org-invitation': OrgInvitationEmailData;
  }
}

/** The templates the email slice registers, after the platform's nine. */
export const EMAIL_SLICE_TEMPLATES: readonly EmailTemplateEntry[] = [
  { name: 'org-invitation', render: orgInvitationEmail as EmailTemplate<never>, registrant: 'identity' },
];

export { orgInvitationEmail };
export type { OrgInvitationEmailData };
