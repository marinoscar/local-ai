// `localai.settings.nodes` (warn, no code): is the fleet policy one a laptop ark can live with?
// (docs/specs/cloud-app.md sections 3.6 and 7.) The platform's defaults prune a node record after 30 days offline and
// call a node stale after 90 seconds; this app seeds 3650 days and 45 seconds (`localai.seed.ts`). If an administrator
// or a fresh database brings back the platform's values, offline laptops are pruned and their history lost.
import { Injectable, type OnModuleInit } from '@nestjs/common';
import { DoctorCheckRegistry, type DoctorCheck, type DoctorCheckOutcome } from '@marinoscar/platform-api/doctor';
import { SystemSettingsService } from '@marinoscar/platform-api/settings';

import { buildOutcome } from './doctor-outcome';
import { LOCALAI_DOCTOR_CATEGORY, MAX_STALE_HEARTBEAT_SECONDS, MIN_OFFLINE_RETENTION_DAYS } from './doctor.constants';

@Injectable()
export class SettingsNodesDoctorCheck implements DoctorCheck, OnModuleInit {
  readonly id = 'localai.settings.nodes';
  readonly category = LOCALAI_DOCTOR_CATEGORY;
  readonly label = 'Node fleet settings suit offline laptops';
  readonly timeoutMs = 3_000;

  constructor(
    private readonly registry: DoctorCheckRegistry,
    private readonly settings: SystemSettingsService,
  ) {}

  onModuleInit(): void {
    this.registry.register(this);
  }

  async run(): Promise<DoctorCheckOutcome> {
    const { offlineRetentionDays, staleHeartbeatSeconds } = await this.settings.getNamespace('nodes');
    const data = { offlineRetentionDays, staleHeartbeatSeconds };
    const problems: string[] = [];
    const fixes: string[] = [];
    if (offlineRetentionDays < MIN_OFFLINE_RETENTION_DAYS) {
      problems.push(`offline nodes are forgotten after ${offlineRetentionDays} days`);
      fixes.push(`set "Keep offline nodes" to at least ${MIN_OFFLINE_RETENTION_DAYS} days (this app seeds 3650)`);
    }
    if (staleHeartbeatSeconds > MAX_STALE_HEARTBEAT_SECONDS) {
      problems.push(`a node counts as stale only after ${staleHeartbeatSeconds} seconds`);
      fixes.push(`set the stale heartbeat window to ${MAX_STALE_HEARTBEAT_SECONDS} seconds or less (this app seeds 45)`);
    }
    if (problems.length === 0) {
      return buildOutcome({ status: 'pass', detail: `Offline nodes are kept ${offlineRetentionDays} days; stale after ${staleHeartbeatSeconds} seconds`, data });
    }
    return buildOutcome({
      status: 'warn',
      detail: `The node fleet settings do not suit a lab with offline laptops: ${problems.join('; ')}`,
      remedy: `In the system settings for worker nodes ("nodes" namespace; Admin → Worker nodes when that page is mounted), ${fixes.join(' and ')}`,
      data,
    });
  }
}
