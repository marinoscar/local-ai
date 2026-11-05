// `localai.gateway.keys` (LAI-GW-043, warning): a node in the routing table whose stored llama key the node refused
// (`keyInvalid`), so the gateway cannot use it until the node re-reports its key. (docs/specs/gateway.md section 7.)
// Read-only: it reads the in-memory table; the node ids it names are not secret.
import { Injectable, type OnModuleInit } from '@nestjs/common';
import { DoctorCheckRegistry, type DoctorCheck, type DoctorCheckOutcome } from '@marinoscar/platform-api/doctor';

import { buildOutcome, nameList } from '../../doctor/doctor-outcome';
import { LOCALAI_DOCTOR_CATEGORY } from '../../doctor/doctor.constants';
import { RoutingTable } from '../routing/routing-table';
import { GATEWAY_CODES } from './gateway-codes';

@Injectable()
export class GatewayKeysDoctorCheck implements DoctorCheck, OnModuleInit {
  readonly id = 'localai.gateway.keys';
  readonly category = LOCALAI_DOCTOR_CATEGORY;
  readonly label = "The gateway's copy of every node key works";
  readonly timeoutMs = 3_000;

  constructor(
    private readonly registry: DoctorCheckRegistry,
    private readonly table: Pick<RoutingTable, 'keyInvalidNodes'>,
  ) {}

  onModuleInit(): void {
    this.registry.register(this);
  }

  run(): Promise<DoctorCheckOutcome> {
    const nodes = [...this.table.keyInvalidNodes()].sort();
    if (nodes.length === 0) return Promise.resolve(buildOutcome({ status: 'pass', detail: 'No node refused the stored key', data: { count: 0 } }));
    return Promise.resolve(buildOutcome({ status: 'warn', code: GATEWAY_CODES.nodeKeyInvalid, found: `${nameList(nodes)} refused the stored key`, data: { count: nodes.length } }));
  }
}
