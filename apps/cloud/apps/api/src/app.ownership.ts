// The app's user-owned and org-owned models, as data for the platform's
// registries (`platform/registrations.ts`). Empty until the first Local AI
// fragment adds a model with an owner column (`prisma/fragments/`); the
// `userOwnedData` conformance suite then fails until the model has an entry
// here and a keep-or-delete decision in `platform/user-data/user-data.manifest.ts`.
import type { ModelOwnershipDef, UserOwnedModelDef } from '@marinoscar/platform-api/core';

export const APP_USER_OWNED_MODELS: readonly UserOwnedModelDef[] = [];

export const APP_MODEL_OWNERSHIP: readonly ModelOwnershipDef[] = [];
