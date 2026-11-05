// `localai.gateway.usage` (LAI-GW-060, warning): the usage recorder dropped rows in the last hour because its database
// write failed twice. (docs/specs/gateway.md sections 4.5 and 7.) Requests were not affected; the usage page misses
// those rows. Read-only: it reads the recorder's in-memory counter, so it reports what THIS API process dropped (a
// restart clears the count).
import { Injectable, type OnModuleInit } from '@nestjs/common';
import { DoctorCheckRegistry, type DoctorCheck, type DoctorCheckOutcome } from '@marinoscar/platform-api/doctor';

import { buildOutcome } from '../../doctor/doctor-outcome';
import { LOCALAI_DOCTOR_CATEGORY } from '../../doctor/doctor.constants';
import type { UsageHealth } from '../usage/usage-recorder';
import { GATEWAY_CODES } from './gateway-codes';

export const USAGE_DROP_WINDOW_MS = 3_600_000;

@Injectable()
export class GatewayUsageDoctorCheck implements DoctorCheck, OnModuleInit {
  readonly id = 'localai.gateway.usage';
  readonly category = LOCALAI_DOCTOR_CATEGORY;
  readonly label = 'The gateway saves its usage records';
  readonly timeoutMs = 3_000;

  constructor(
    private readonly registry: DoctorCheckRegistry,
    private readonly recorder: Pick<UsageHealth, 'droppedSince'>,
  ) {}

  onModuleInit(): void {
    this.registry.register(this);
  }

  run(): Promise<DoctorCheckOutcome> {
    const dropped = this.recorder.droppedSince(USAGE_DROP_WINDOW_MS);
    if (dropped === 0) return Promise.resolve(buildOutcome({ status: 'pass', detail: 'No usage record was dropped in the last hour', data: { dropped: 0 } }));
    return Promise.resolve(buildOutcome({ status: 'warn', code: GATEWAY_CODES.usageDropped, found: `${dropped} dropped in the last hour`, data: { dropped } }));
  }
}

