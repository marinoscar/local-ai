// The email slice: the SES and SMTP transports, the admin email settings
// (`/api/email-settings`, `/admin/settings/email`) and the template registry.
// The identity notices (welcome, role change, invitations) are only logged
// until the notifications slice turns them into mail.
import type { ApiSlice } from '../slices/slice';

export const emailSlice: ApiSlice = {
  id: 'email',
  contribute: () => {
    const email = require('@marinoscar/platform-api/email') as typeof import('@marinoscar/platform-api/email');
    return { credentialPurposes: [email.SES_CREDENTIAL_PURPOSE_DEF, email.SMTP_CREDENTIAL_PURPOSE_DEF] };
  },
  register: () => {
    const email = require('@marinoscar/platform-api/email') as typeof import('@marinoscar/platform-api/email');
    const { EMAIL_MODULE_OPTIONS } = require('./email.options') as typeof import('./email.options');
    const { EMAIL_SLICE_TEMPLATES } = require('./templates') as typeof import('./templates');
    const { contributions } = require('../slices/contributions') as typeof import('../slices/contributions');
    // The render context first, with the options forRoot receives, so a
    // template rendered before Nest composes anything names the product.
    email.configureEmailRendering(EMAIL_MODULE_OPTIONS);
    email.registerPlatformEmailTemplates();
    email.registerEmailTemplates(EMAIL_SLICE_TEMPLATES);
    // Templates other slices own the words of (none today).
    email.registerEmailTemplates(contributions().emailTemplates);
  },
  modules: () => {
    const { EmailModule } = require('./email.config') as typeof import('./email.config');
    return [EmailModule];
  },
};
