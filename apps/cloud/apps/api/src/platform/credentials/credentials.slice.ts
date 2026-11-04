// The credentials slice: the encrypted credential store in three tiers (the
// deployment's, a user's own, an organization's). Storage, email, notifications
// and AI keep their secrets in it, so they `require` it. It has no routes.
import type { ApiSlice } from '../slices/slice';

export const credentialsSlice: ApiSlice = {
  id: 'credentials',
  contribute: () => {
    const { APP_CREDENTIAL_PURPOSES } = require('./credential-purposes') as typeof import('./credential-purposes');
    return { credentialPurposes: APP_CREDENTIAL_PURPOSES };
  },
  modules: () => {
    const { credentialsModules } = require('./credentials.config') as typeof import('./credentials.config');
    return credentialsModules;
  },
};
