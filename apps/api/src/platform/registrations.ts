// Fills the platform's static registries once, at import time, before
// bootstrap: roles and permissions (the mounted slices' and the app's), every
// platform model with a foreign key to `User` plus the app's, and every
// platform model's ownership kind plus the app's. Platform entries register
// first, so a collision names the app. Imported for its side effect by
// `src/prisma/prisma.service.ts` (before the first scoped client) and by the
// conformance spec.
import {
  registerPlatformModelOwnership,
  registerPlatformPermissions,
  registerPlatformUserOwnedModels,
} from '@marinoscar/platform-api/manifest';

import { APP_MODEL_OWNERSHIP, APP_USER_OWNED_MODELS } from '../app.ownership';
import { LOCALAI_MODEL_OWNERSHIP, LOCALAI_USER_OWNED_MODELS } from '../localai/localai.ownership';
import { PERMISSION_OPTIONS } from './permissions';

registerPlatformPermissions(PERMISSION_OPTIONS);
registerPlatformUserOwnedModels([...APP_USER_OWNED_MODELS, ...LOCALAI_USER_OWNED_MODELS]);
registerPlatformModelOwnership([...APP_MODEL_OWNERSHIP, ...LOCALAI_MODEL_OWNERSHIP]);
