// `localai.catalog.import`: downloads a `catalog-<version>` release archive, extracts `catalog/**`, stores the draft
// and queues its lint (docs/specs/cloud-app.md section 4.6). Server-only: it carries neither `nodeResultSchema` nor
// `persistNodeResult`, so it is never offered to a worker node (the work is a network round trip and a database
// write the cloud itself must do).
//
// Single attempt: every failure is deterministic (no such tag, a refused archive, an existing version) or is cheap
// for the administrator to repeat; a silent retry could only repeat the same download. The failure message is in the
// job list (`lastError`) and never carries a path from the archive beyond a short, escaped fragment.
import { Injectable, type OnModuleInit } from '@nestjs/common';
import { JobHandlerRegistry, type Job, type JobHandler } from '@marinoscar/platform-api/jobs';
import { z } from 'zod';

import { JOB_CATALOG_IMPORT } from './catalog.constants';
import { CatalogService } from './catalog.service';

const PayloadSchema = z.object({ tag: z.string(), actorUserId: z.string().nullable() });

@Injectable()
export class CatalogImportJob implements JobHandler, OnModuleInit {
  readonly type = JOB_CATALOG_IMPORT;
  readonly label = 'Catalog import';
  readonly profile = { maxRuntimeMs: 120_000, maxAttempts: 1 } as const;

  constructor(
    private readonly registry: JobHandlerRegistry,
    private readonly catalog: CatalogService,
  ) {}

  onModuleInit(): void {
    this.registry.register(this);
  }

  async process(job: Job): Promise<void> {
    const payload = PayloadSchema.parse(job.payload);
    await this.catalog.importFromArchive(payload.tag, payload.actorUserId);
  }
}
