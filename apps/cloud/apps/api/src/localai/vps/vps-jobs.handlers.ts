// The two node-eligible job types of the VPS stack-agent (docs/specs/cloud-app.md section 4.10):
// `localai.vps.publish_node` and `localai.vps.unpublish_node`, params `{ nodeId }`.
//
// NODE-ELIGIBLE BY DERIVATION: each carries `nodeResultSchema` + `persistNodeResult`, and a `nodeSecretBroker` for the
// Route53 key and the node's tailnet address (never in the params). Only the worker started with
// `--types localai.vps.publish_node,localai.vps.unpublish_node` (the stack-agent on the VPS) lists them. `process` is
// what the API server's own worker would run if it claimed one: it cannot do this work and says so (the runbook sets
// `JOBS_WORKER_MODE=system`, which keeps the server off node-eligible types).
//
// Single attempt: the work touches DNS, a certificate and the shared proxy, and every step is idempotent, so the
// operator presses the button again rather than the queue retrying silently. The job type strings are permanent.
import { Injectable, type OnModuleInit } from '@nestjs/common';
import { JobHandlerRegistry, type Job, type JobHandler } from '@marinoscar/platform-api/jobs';
import type { z } from 'zod';

import { PublishResultSchema, UnpublishResultSchema, type PublishResult, type UnpublishResult } from './vps-result.schema';
import { VpsSecretBroker } from './vps-secret.broker';
import { VpsStateService } from './vps-state.service';
import { JOB_VPS_PUBLISH, JOB_VPS_UNPUBLISH } from './vps.constants';

const SERVER_CANNOT = 'This job runs only on the VPS stack-agent (laictl node start --types …). Set JOBS_WORKER_MODE=system on the API so the server leaves it alone.';

@Injectable()
export class VpsPublishHandler implements JobHandler, OnModuleInit {
  readonly type = JOB_VPS_PUBLISH;
  readonly label = 'Publish node on the VPS';
  /** DNS propagation and the certificate can each take minutes. */
  readonly profile = { maxRuntimeMs: 15 * 60_000, maxAttempts: 1 } as const;
  readonly nodeResultSchema: z.ZodType = PublishResultSchema;

  constructor(
    private readonly registry: JobHandlerRegistry,
    private readonly broker: VpsSecretBroker,
    private readonly state: VpsStateService,
  ) {}

  /** Presence is the declaration (a getter: a constructor parameter is not yet set when a field initialiser runs). */
  get nodeSecretBroker(): VpsSecretBroker {
    return this.broker;
  }

  onModuleInit(): void {
    this.registry.register(this);
  }

  async process(): Promise<void> {
    throw new Error(SERVER_CANNOT);
  }

  async persistNodeResult(job: Job, result: unknown): Promise<void> {
    await this.state.persist(job, 'publish', result as PublishResult);
  }
}

@Injectable()
export class VpsUnpublishHandler implements JobHandler, OnModuleInit {
  readonly type = JOB_VPS_UNPUBLISH;
  readonly label = 'Unpublish node from the VPS';
  readonly profile = { maxRuntimeMs: 5 * 60_000, maxAttempts: 1 } as const;
  readonly nodeResultSchema: z.ZodType = UnpublishResultSchema;

  constructor(
    private readonly registry: JobHandlerRegistry,
    private readonly broker: VpsSecretBroker,
    private readonly state: VpsStateService,
  ) {}

  /** Presence is the declaration (a getter: a constructor parameter is not yet set when a field initialiser runs). */
  get nodeSecretBroker(): VpsSecretBroker {
    return this.broker;
  }

  onModuleInit(): void {
    this.registry.register(this);
  }

  async process(): Promise<void> {
    throw new Error(SERVER_CANNOT);
  }

  async persistNodeResult(job: Job, result: unknown): Promise<void> {
    await this.state.persist(job, 'unpublish', result as UnpublishResult);
  }
}
