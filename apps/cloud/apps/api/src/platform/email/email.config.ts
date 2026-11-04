// The email slice, configured once: the SES and SMTP transports,
// `/api/email-settings`, the template registry and layout, and the Doctor
// check. A feature that sends mail imports this one object
// (`imports: [EmailModule]`) and injects `EmailSettingsService` and the providers.
import { EmailModule as PlatformEmailModule } from '@marinoscar/platform-api/email';

import { EMAIL_MODULE_OPTIONS } from './email.options';

export const EmailModule = PlatformEmailModule.forRoot(EMAIL_MODULE_OPTIONS);
