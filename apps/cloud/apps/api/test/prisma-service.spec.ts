// `PrismaService` is the app's `PLATFORM_PRISMA` (src/platform/platform.ts). The platform slices call scoped clients
// on it that the `useExisting` binding does not type-check, so a missing one is a 500 at run time, not a build error
// ("this.prisma.forOrg is not a function" on the AI usage report, EnterpriseAppBase #1155). Every method a slice
// calls is pinned here.
import { PrismaService } from '../src/prisma/prisma.service';

describe('PrismaService, the PLATFORM_PRISMA port', () => {
  it.each(['forUser', 'forOrg', 'runInOrg'])('implements %s', (method) => {
    expect(typeof (PrismaService.prototype as unknown as Record<string, unknown>)[method]).toBe('function');
  });
});
