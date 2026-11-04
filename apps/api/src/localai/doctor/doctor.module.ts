// The cloud's Doctor checks (docs/specs/cloud-app.md section 7), one provider per check. Each registers itself with the
// platform's `DoctorCheckRegistry` (global, provided by `DoctorModule.forRoot`) in `onModuleInit`, so all seven appear
// on `/admin/settings/doctor`. `PrismaService` and `SystemSettingsService` are global; the credentials slice is
// imported for `CredentialsService`.
import { Module } from '@nestjs/common';
import { CredentialsModule } from '@marinoscar/platform-api/credentials';

import { AictlDoctorCheck, ExecAictlProbe } from './aictl.check';
import { CatalogApprovedDoctorCheck } from './catalog-approved.check';
import { CommandsStuckDoctorCheck } from './commands-stuck.check';
import { CredentialsDoctorCheck, PlatformCipherProbe } from './credentials.check';
import { LOCALAI_DOCTOR_AICTL, LOCALAI_DOCTOR_CIPHER } from './doctor.constants';
import { NodesKeysDoctorCheck } from './nodes-keys.check';
import { NodesPendingDoctorCheck } from './nodes-pending.check';
import { SettingsNodesDoctorCheck } from './settings-nodes.check';

/** Every check class, in the order the report lists them within the category. */
export const LOCALAI_DOCTOR_CHECKS = [
  CredentialsDoctorCheck,
  AictlDoctorCheck,
  CatalogApprovedDoctorCheck,
  NodesPendingDoctorCheck,
  CommandsStuckDoctorCheck,
  NodesKeysDoctorCheck,
  SettingsNodesDoctorCheck,
] as const;

@Module({
  imports: [CredentialsModule],
  providers: [
    { provide: LOCALAI_DOCTOR_CIPHER, useFactory: () => new PlatformCipherProbe() },
    { provide: LOCALAI_DOCTOR_AICTL, useFactory: () => new ExecAictlProbe() },
    ...LOCALAI_DOCTOR_CHECKS,
  ],
})
export class LocalaiDoctorModule {}
