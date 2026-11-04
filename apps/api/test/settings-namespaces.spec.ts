// The app's composition registers every system settings namespace it reads,
// in the order the request bodies are composed: the app's own `storage` first
// (src/platform/platform.ts imports its declaration first), then `jobs`,
// which `JobsModule.forRoot()` registers itself. Without `jobs` the nightly
// job-history purge would find no policy, and a PATCH of `jobs` would be
// dropped by the validation pipe.
import { DEFAULT_JOBS_POLICY } from '@marinoscar/platform-api/jobs';
import {
  composeDefaultSystemSettings,
  composePatchSystemSettingsSchema,
  systemSettingsNamespaceRegistry,
} from '@marinoscar/platform-api/settings';

import '../src/platform/platform';

describe('system settings namespaces', () => {
  it('registers storage, then jobs (from JobsModule.forRoot())', () => {
    const ids = systemSettingsNamespaceRegistry.ids();
    expect(ids).toEqual(expect.arrayContaining(['storage', 'jobs']));
    expect(ids.indexOf('storage')).toBeLessThan(ids.indexOf('jobs'));
  });

  it('defaults jobs to the shipped policy, so the job-history purge has one', () => {
    expect(composeDefaultSystemSettings()).toMatchObject({ jobs: DEFAULT_JOBS_POLICY });
  });

  it('accepts both namespaces in a PATCH /api/system-settings body', () => {
    const body = { storage: composeDefaultSystemSettings().storage, jobs: { history: { purgeEnabled: false } } };
    expect(composePatchSystemSettingsSchema().parse(body)).toEqual(body);
  });
});
