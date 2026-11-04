// =============================================================================
// The user-data manifest: what a data reset keeps and deletes (issue #880)
// =============================================================================
//
// The platform's categories, model hints and factory reset steps come first
// (`registerPlatformUserData()`); everything below is the app's DATA. This is
// the only file you edit when a model gains an owner column or the reset needs
// an extra step: the Danger Zone, the factory reset, the organization
// offboarding and the data export pick the entries up with no code change.
// The `userData` conformance suite (test/conformance.spec.ts) and the Doctor's
// `user-data.registries` check fail until every user-owned model (see
// `src/app.ownership.ts`) has an entry here.
// =============================================================================

import {
  registerFactoryResetStep,
  registerOffboardingPrecondition,
  registerPlatformUserData,
  registerUserDataCategory,
  registerUserDataModels,
  registerUserDataScope,
  type FactoryResetStepDef,
  type OffboardingPreconditionDef,
  type UserDataCategoryDef,
  type UserDataModelHint,
  type UserDataScopeDef,
} from '@marinoscar/platform-api/user-data';
import type { Prisma } from '@prisma/client';

/** The app's categories: the Danger Zone rows, and what a scope selects. */
export const APP_USER_DATA_CATEGORIES: readonly UserDataCategoryDef[] = [];

/** One keep-or-delete decision per user-owned model. */
export const APP_USER_DATA_MODELS: readonly UserDataModelHint<Prisma.ModelName>[] = [
  // Lab infrastructure (the user-owned Local AI models; the other two have no user column): the user columns are actors (SetNull), so a user purge detaches them and never deletes the rows.
  { model: 'LocalaiNode', keep: 'Lab node state: deployment infrastructure that outlives the user who added it.' },
  { model: 'LocalaiCatalogVersion', keep: 'Lab model catalog: a deployment-wide version history.' },
  { model: 'LocalaiCommand', keep: 'Lab command history: an audit view of what was asked of a node.' },
];

/** Narrow scopes, such as "delete my saved views" (none by default: the built-in scopes cover every category). */
export const APP_USER_DATA_SCOPES: readonly UserDataScopeDef[] = [];

/** Extra factory reset steps, for data the model registry cannot describe. */
export const APP_FACTORY_RESET_STEPS: readonly FactoryResetStepDef[] = [];

/** Checks that must pass before an organization may be offboarded. */
export const APP_OFFBOARDING_PRECONDITIONS: readonly OffboardingPreconditionDef[] = [];

registerPlatformUserData();
APP_USER_DATA_CATEGORIES.forEach(registerUserDataCategory);
registerUserDataModels(APP_USER_DATA_MODELS);
APP_USER_DATA_SCOPES.forEach(registerUserDataScope);
APP_FACTORY_RESET_STEPS.forEach(registerFactoryResetStep);
APP_OFFBOARDING_PRECONDITIONS.forEach(registerOffboardingPrecondition);
