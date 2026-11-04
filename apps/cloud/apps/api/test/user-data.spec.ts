// The data resets are wired with manifest entries only (issue #880): the
// composed app resolves the user-data slice, plans the delete order from this
// app's schema, includes the platform's user-owned
// models (the app adds none yet: src/platform/user-data/user-data.manifest.ts) and the Doctor check passes.

import { Test } from '@nestjs/testing';
import { UserDataPlanService, UserDataRegistriesDoctorCheck } from '@marinoscar/platform-api/user-data';

import { AppModule } from '../src/app.module';

describe('the user-data slice in this app', () => {
  it('plans the purge of a user from the app\'s composed schema', async () => {
    const moduleRef = await Test.createTestingModule({ imports: [AppModule] }).compile();
    const plan = moduleRef.get(UserDataPlanService).user();

    expect(plan.steps.map((step) => step.model)).toContain('PersonalAccessToken');
    await moduleRef.close();
  });

  it('passes the registries check: every owner model has a decision', async () => {
    const moduleRef = await Test.createTestingModule({ imports: [AppModule] }).compile();
    const outcome = await moduleRef.get(UserDataRegistriesDoctorCheck).run();

    expect(outcome).toMatchObject({ status: 'pass' });
    await moduleRef.close();
  });
});
