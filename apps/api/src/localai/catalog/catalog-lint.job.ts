// `localai.catalog.lint`: runs `aictl catalog lint --json` over a stored version and records the verdict
// (docs/specs/cloud-app.md section 4.6). Server-only for the same reason as the import: it starts a local binary of
// the API image, which no worker node has.
//
// The job RETURNS normally when the linter could not run: that is a recorded outcome (the version stays `draft` with
// `lintResult.error` and doctor code LAI-CLD-033), not a reason to retry the same missing binary. Single attempt.
import { Injectable, type OnModuleInit } from '@nestjs/common';
import { JobHandlerRegistry, type Job, type JobHandler } from '@marinoscar/platform-api/jobs';
import { z } from 'zod';

import { JOB_CATALOG_LINT } from './catalog.constants';
import { CatalogService } from './catalog.service';

const PayloadSchema = z.object({ version: z.string(), actorUserId: z.string().nullable() });

@Injectable()
export class CatalogLintJob implements JobHandler, OnModuleInit {
  readonly type = JOB_CATALOG_LINT;
  readonly label = 'Catalog lint';
  // 30 s for aictl plus the temp-directory writes and the row updates.
  readonly profile = { maxRuntimeMs: 90_000, maxAttempts: 1 } as const;

  constructor(
    private readonly registry: JobHandlerRegistry,
    private readonly catalog: CatalogService,
  ) {}

  onModuleInit(): void {
    this.registry.register(this);
  }

  async process(job: Job): Promise<void> {
    const payload = PayloadSchema.parse(job.payload);
    await this.catalog.runLint(payload.version, payload.actorUserId);
  }
}
