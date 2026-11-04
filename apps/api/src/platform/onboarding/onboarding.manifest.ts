// =============================================================================
// The onboarding manifest: the facts and steps this app has
// =============================================================================
//
// Registered at import time by `./onboarding.slice.ts`, before bootstrap. A
// FACT is the only thing that may read anything (the database, the Doctor, a
// feature switch) and is resolved at most once per request, only when a step
// that applies needs it; a STEP is data plus two synchronous functions over
// facts. A step id is permanent (it is stored in a user's `skipped` list).
//
// The platform ships nine steps; this app keeps the ones whose page exists in
// the slices it mounts (a step that links to `/admin/settings/storage` makes no
// sense without the storage slice), then adds its own.
// =============================================================================

import {
  ONBOARDING_STEP_IDS,
  PLATFORM_ONBOARDING_FACTS,
  PLATFORM_ONBOARDING_STEPS,
  registerOnboardingFact,
  registerOnboardingStep,
  type OnboardingFactDef,
  type OnboardingStepDef,
} from '@marinoscar/platform-api/onboarding';

import type { EnabledSlices, SliceId } from '../slices/slice';

/** The slice whose page a platform step links to; a step not listed is always kept. */
const STEP_OWNER: Readonly<Record<string, SliceId>> = {
  [ONBOARDING_STEP_IDS.STORAGE]: 'storage',
  [ONBOARDING_STEP_IDS.EMAIL]: 'email',
  [ONBOARDING_STEP_IDS.AI]: 'ai',
  [ONBOARDING_STEP_IDS.PUSH]: 'notifications',
  [ONBOARDING_STEP_IDS.BACKUP]: 'db-backup',
  // The profile page (name and picture) ships with the storage slice's web side.
  [ONBOARDING_STEP_IDS.PROFILE]: 'storage',
  [ONBOARDING_STEP_IDS.NOTIFICATIONS]: 'notifications',
};

/** The app's own facts (the only place that may read anything). None yet. */
export const APP_ONBOARDING_FACTS: readonly OnboardingFactDef[] = [];

/** The app's own steps. None yet; a step id is permanent once a user can skip it. */
export const APP_ONBOARDING_STEPS: readonly OnboardingStepDef[] = [];

/** Registers the platform's facts and the steps that fit `enabled`, then the app's. */
export function registerOnboardingManifest(enabled: EnabledSlices): void {
  registerOnboardingFact(...PLATFORM_ONBOARDING_FACTS);
  registerOnboardingStep(...PLATFORM_ONBOARDING_STEPS.filter((step) => {
    const owner = STEP_OWNER[step.id];
    return owner === undefined || enabled.has(owner);
  }));
  if (APP_ONBOARDING_FACTS.length > 0) registerOnboardingFact(...APP_ONBOARDING_FACTS);
  if (APP_ONBOARDING_STEPS.length > 0) registerOnboardingStep(...APP_ONBOARDING_STEPS);
}
