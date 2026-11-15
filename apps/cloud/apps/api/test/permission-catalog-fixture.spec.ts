// Proves `fixtures/permission-catalog.json` is exactly the permission catalog this app seeds (`prisma/seed.ts` composes
// it from the same `PERMISSION_OPTIONS`). The web app's platform conformance (`apps/web/src/__tests__/conformance.test.ts`,
// the `settings-ai-cards` suite) reads the JSON as the API's permission ids, so a card can only name a permission the
// API really declares. Regenerate after an intended change (a slice turned on or off, a new permission) with
// `UPDATE_PERMISSION_CATALOG=1 npm test --workspace=api -- permission-catalog-fixture`.
import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

import { platformPermissionCatalog } from '@marinoscar/platform-api/manifest';

import { PERMISSION_OPTIONS } from '../src/platform/permissions';

const FILE = join(__dirname, 'fixtures', 'permission-catalog.json');

describe('the permission catalog fixture', () => {
  it('equals the catalog the seed writes (names and scopes, sorted)', () => {
    const permissions = platformPermissionCatalog(PERMISSION_OPTIONS)
      .permissions.map(({ name, scope }) => ({ name, scope }))
      .sort((a, b) => a.name.localeCompare(b.name));
    const fixture = { permissions };
    if (process.env.UPDATE_PERMISSION_CATALOG === '1') writeFileSync(FILE, `${JSON.stringify(fixture, null, 2)}\n`);
    expect(fixture).toEqual(JSON.parse(readFileSync(FILE, 'utf8')));
  });
});
