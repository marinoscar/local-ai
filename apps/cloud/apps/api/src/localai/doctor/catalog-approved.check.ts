// `localai.catalog.approved` (warn, no code of its own): is there a catalog version nodes can download?
// (docs/specs/cloud-app.md sections 4.6 and 7.) Until one version is `approved`, `GET /api/nodes/{id}/localai/catalog`
// has nothing to serve and every node keeps the catalog it has. The text of LAI-CLD-013 (what a node reports when the
// download fails) is the hint, as the spec orders; the check carries no code because nothing has failed yet.
import { Injectable, type OnModuleInit } from '@nestjs/common';
import { DoctorCheckRegistry, type DoctorCheck, type DoctorCheckOutcome } from '@marinoscar/platform-api/doctor';

import { PrismaService } from '../../prisma/prisma.service';
import { CLOUD_CODES } from './codes';
import { buildOutcome } from './doctor-outcome';
import { LOCALAI_DOCTOR_CATEGORY } from './doctor.constants';

@Injectable()
export class CatalogApprovedDoctorCheck implements DoctorCheck, OnModuleInit {
  readonly id = 'localai.catalog.approved';
  readonly category = LOCALAI_DOCTOR_CATEGORY;
  readonly label = 'An approved catalog exists';
  readonly timeoutMs = 3_000;

  constructor(
    private readonly registry: DoctorCheckRegistry,
    private readonly prisma: PrismaService,
  ) {}

  onModuleInit(): void {
    this.registry.register(this);
  }

  async run(): Promise<DoctorCheckOutcome> {
    const approved = await this.prisma.localaiCatalogVersion.findFirst({ where: { status: 'approved' }, select: { version: true } });
    if (approved) {
      return buildOutcome({ status: 'pass', detail: `Catalog ${approved.version} is approved`, data: { approvedVersion: approved.version } });
    }
    const hint = CLOUD_CODES.catalogUnavailable;
    return buildOutcome({
      status: 'warn',
      detail: "No catalog version is approved, so nodes can't download one",
      remedy: `Import a catalog version and approve it (Lab → Catalog). Until then, nodes report "${hint.title}" (${hint.code}): ${hint.fix}`,
      data: { approvedVersion: null },
    });
  }
}
