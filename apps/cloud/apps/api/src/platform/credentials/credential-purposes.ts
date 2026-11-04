// The app's credential purposes: what the credential stores accept beyond the
// platform's (storage, email, notifications and AI contribute theirs). A
// purpose is the cipher's sub-key domain, so it is PERMANENT once a row exists:
// add one, never rename one. Registered by `slices/register.ts` before
// bootstrap; the stores refuse a write to an unregistered purpose.
import type { CredentialPurposeDef } from '@marinoscar/platform-api/credentials';

/** Every purpose this app adds. Append here. */
export const APP_CREDENTIAL_PURPOSES: readonly CredentialPurposeDef[] = [];
