// `localai.gateway.routable` (LAI-GW-021, warning): the routing table has no routable node (none online with a stored key
// that is not suspect or refused), so no model can be served. (docs/specs/gateway.md section 7.) Read-only: it reads
// the in-memory table.
import { Injectable, type OnModuleInit } from '@nestjs/common';
import { DoctorCheckRegistry, type DoctorCheck, type DoctorCheckOutcome } from '@marinoscar/platform-api/doctor';

import { buildOutcome } from '../../doctor/doctor-outcome';
import { LOCALAI_DOCTOR_CATEGORY } from '../../doctor/doctor.constants';
import { RoutingTable } from '../routing/routing-table';
import { GATEWAY_CODES } from './gateway-codes';

@Injectable()
export class GatewayRoutableDoctorCheck implements DoctorCheck, OnModuleInit {
  readonly id = 'localai.gateway.routable';
  readonly category = LOCALAI_DOCTOR_CATEGORY;
  readonly label = 'The gateway has a node to send requests to';
  readonly timeoutMs = 3_000;

  constructor(
    private readonly registry: DoctorCheckRegistry,
    private readonly table: Pick<RoutingTable, 'routableNodeCount'>,
  ) {}

  onModuleInit(): void {
    this.registry.register(this);
  }

  run(): Promise<DoctorCheckOutcome> {
    const count = this.table.routableNodeCount();
    if (count === 0) {
      return Promise.resolve(buildOutcome({ status: 'warn', code: GATEWAY_CODES.noCapacity, found: 'no routable node', data: { count: 0 } }));
    }
    return Promise.resolve(buildOutcome({ status: 'pass', detail: count === 1 ? '1 routable node' : `${count} routable nodes`, data: { count } }));
  }
}
