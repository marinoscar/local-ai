// The onboarding slice: the welcome dialog, the user's Get started checklist
// (`/settings/getting-started`) and the administrator's Setup guide
// (`/admin/settings/setup`), derived from the real state of the deployment
// (settings, Doctor checks, counts), plus aggregate activation metrics. The
// manifest (`./onboarding.manifest.ts`) is the app's whole contribution.
import type { UserSettingsNamespace } from '@marinoscar/platform-api/settings';

import type { ApiSlice } from '../slices/slice';

export const onboardingSlice: ApiSlice = {
  id: 'onboarding',
  rawSql: [
    {
      file: 'platform/onboarding/onboarding-data.adapter.ts',
      why:
        'The activation metrics are aggregates over users, user_settings and push_subscriptions in one statement, ' +
        'with every value bound as a positional parameter; none is request-derived and no table has row-level security.',
    },
  ],
  contribute: () => {
    const { ONBOARDING_USER_SETTINGS } = require('@marinoscar/platform-api/onboarding') as typeof import('@marinoscar/platform-api/onboarding');
    return { userSettings: [ONBOARDING_USER_SETTINGS as UserSettingsNamespace] };
  },
  register: (enabled) => {
    const { registerOnboardingManifest } = require('./onboarding.manifest') as typeof import('./onboarding.manifest');
    registerOnboardingManifest(enabled);
  },
  modules: () => {
    const { onboardingModule } = require('./onboarding.config') as typeof import('./onboarding.config');
    return [onboardingModule];
  },
};
