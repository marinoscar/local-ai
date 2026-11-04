import { describeDoctorCheckConformance } from '@marinoscar/platform-api/doctor/testing';

import { fakePrisma } from '../../../test/localai/support/fake-tables';
import { expectContract } from '../../../test/localai/support/doctor-asserts';
import { CatalogApprovedDoctorCheck } from './catalog-approved.check';

const registry = { register: jest.fn() };
const checkWith = (versions: Array<Record<string, unknown>>) => new CatalogApprovedDoctorCheck(registry as never, fakePrisma({ localaiCatalogVersion: versions }) as never);

describe('localai.catalog.approved', () => {
  it('registers itself', () => {
    const check = checkWith([]);
    check.onModuleInit();
    expect(registry.register).toHaveBeenCalledWith(check);
  });

  it('passes when a version is approved and names it', async () => {
    const outcome = await checkWith([{ version: '2026.11.02-1', status: 'approved' }, { version: '2026.10.30-1', status: 'superseded' }]).run();
    expect(outcome).toMatchObject({ status: 'pass', data: { approvedVersion: '2026.11.02-1' } });
    expect(outcome.detail).toContain('2026.11.02-1');
    expectContract(outcome);
  });

  it.each([
    ['no versions at all', []],
    ['only drafts, valid and superseded ones', [{ version: 'a', status: 'draft' }, { version: 'b', status: 'valid' }, { version: 'c', status: 'invalid' }, { version: 'd', status: 'superseded' }]],
  ])('warns with the LAI-CLD-013 text as the hint: %s', async (_name, versions) => {
    const outcome = await checkWith(versions).run();
    expect(outcome.status).toBe('warn');
    // No code of its own: nothing has failed yet.
    expect(outcome.data?.code).toBeUndefined();
    expect(outcome.remedy).toContain("Couldn't download the catalog from the cloud");
    expect(outcome.remedy).toContain('LAI-CLD-013');
    expect(outcome.remedy).toContain('The node keeps its current catalog');
    expectContract(outcome);
  });
});

describeDoctorCheckConformance('localai.catalog.approved', ({ prisma }) => new CatalogApprovedDoctorCheck(registry as never, prisma.client), {
  scenarios: [
    { name: 'approved', prisma: { results: { 'localaiCatalogVersion.findFirst': { version: '2026.11.02-1' } } }, expectStatus: 'pass' },
    { name: 'none approved', prisma: { results: { 'localaiCatalogVersion.findFirst': null } }, expectStatus: 'warn' },
  ],
});
