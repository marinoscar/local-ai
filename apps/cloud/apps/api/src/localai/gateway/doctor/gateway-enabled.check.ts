// `localai.gateway.enabled` (LAI-GW-051, warning): the gateway is switched off by `LOCALAI_GATEWAY_ENABLED=false` or the
// site is in maintenance mode, so every `/v1` request answers 503. (docs/specs/gateway.md section 7.) Read-only.
import { Injectable, type OnModuleInit } from '@nestjs/common';
import { DoctorCheckRegistry, type DoctorCheck, type DoctorCheckOutcome } from '@marinoscar/platform-api/doctor';
import { MaintenanceModeService } from '@marinoscar/platform-api/host';

import { buildOutcome, errorName } from '../../doctor/doctor-outcome';
import { LOCALAI_DOCTOR_CATEGORY } from '../../doctor/doctor.constants';
import { GatewaySettingsReader } from '../gateway-settings';
import { GATEWAY_CODES } from './gateway-codes';

@Injectable()
export class GatewayEnabledDoctorCheck implements DoctorCheck, OnModuleInit {
  readonly id = 'localai.gateway.enabled';
  readonly category = LOCALAI_DOCTOR_CATEGORY;
  readonly label = 'The gateway accepts requests';
  readonly timeoutMs = 3_000;

  constructor(
    private readonly registry: DoctorCheckRegistry,
    private readonly settings: Pick<GatewaySettingsReader, 'enabled'>,
    private readonly maintenance: Pick<MaintenanceModeService, 'isEnabled'>,
  ) {}

  onModuleInit(): void {
    this.registry.register(this);
  }

  async run(): Promise<DoctorCheckOutcome> {
    if (!this.settings.enabled()) {
      return buildOutcome({ status: 'warn', code: GATEWAY_CODES.unavailable, found: 'LOCALAI_GATEWAY_ENABLED is false', data: { enabled: false, maintenance: false } });
    }
    let maintenance: boolean;
    try {
      maintenance = await this.maintenance.isEnabled();
    } catch (error) {
      return buildOutcome({ status: 'skip', detail: 'The maintenance state could not be read', error: errorName(error) });
    }
    if (maintenance) {
      return buildOutcome({ status: 'warn', code: GATEWAY_CODES.unavailable, found: 'maintenance mode is on', data: { enabled: true, maintenance: true } });
    }
    return buildOutcome({ status: 'pass', detail: 'The gateway is on and the site is not in maintenance', data: { enabled: true, maintenance: false } });
  }
}
