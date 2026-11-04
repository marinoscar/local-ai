// The credentials slice's three modules. None is `@Global()` on purpose: a
// feature that reads a plaintext secret imports the module it needs, so every
// such reader is a visible line in a diff. They are listed in the root module
// so a broken provider graph fails at boot. Needs no host port of its own:
// it reaches the database through core's `PLATFORM_PRISMA`.
import { CredentialsModule, OrgCredentialsModule, UserCredentialsModule } from '@marinoscar/platform-api/credentials';

export const credentialsModules = [CredentialsModule, UserCredentialsModule, OrgCredentialsModule] as const;
