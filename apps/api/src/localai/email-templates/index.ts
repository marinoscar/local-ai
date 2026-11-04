import type { EmailTemplate, EmailTemplateEntry } from '@marinoscar/platform-api/email';

import { alertRaisedEmail, type AlertRaisedEmailData } from './alert-raised.email';
import { nodeEnrolledEmail, type NodeEnrolledEmailData } from './node-enrolled.email';

// =============================================================================
// The email templates the Local AI cloud owns the words of
// =============================================================================
//
// A template name is a stable id: notification events map to it (`../localai.notifications.ts`). Registered by the email
// slice (`src/platform/email/templates/index.ts` spreads this list) at import time, never from `onModuleInit`.
// =============================================================================

declare module '@marinoscar/platform-api/email' {
  interface EmailTemplateDataMap {
    'localai-alert-raised': AlertRaisedEmailData;
    'localai-node-enrolled': NodeEnrolledEmailData;
  }
}

export const LOCALAI_EMAIL_TEMPLATES: readonly EmailTemplateEntry[] = [
  { name: 'localai-alert-raised', render: alertRaisedEmail as EmailTemplate<never>, registrant: 'localai' },
  { name: 'localai-node-enrolled', render: nodeEnrolledEmail as EmailTemplate<never>, registrant: 'localai' },
];

export { alertRaisedEmail, nodeEnrolledEmail };
export type { AlertRaisedEmailData, NodeEnrolledEmailData };
