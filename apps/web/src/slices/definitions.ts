// Every optional slice the starter can mount, as data. Importing this file
// renders and registers nothing (pages are lazy), so a slice that is not in
// `packages/shared/slices.json` costs a disabled app nothing at run time. To
// remove a slice's code for good, delete its `src/slices/<id>.tsx` and its line
// below (and its id from the catalog in `slices.json`).
import { aiWebSlice } from './ai';
import { credentialsWebSlice } from './credentials';
import { dbBackupWebSlice } from './db-backup';
import { emailWebSlice } from './email';
import { notificationsWebSlice } from './notifications';
import { onboardingWebSlice } from './onboarding';
import { storageWebSlice } from './storage';
import { telemetryWebSlice } from './telemetry';
import type { SliceId, WebSlice } from './slice';

export const ALL_WEB_SLICES: Readonly<Record<SliceId, WebSlice>> = {
  credentials: credentialsWebSlice,
  storage: storageWebSlice,
  email: emailWebSlice,
  notifications: notificationsWebSlice,
  ai: aiWebSlice,
  'db-backup': dbBackupWebSlice,
  onboarding: onboardingWebSlice,
  telemetry: telemetryWebSlice,
};
