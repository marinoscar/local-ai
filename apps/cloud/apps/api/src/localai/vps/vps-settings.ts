// The `localai` settings the stack-agent reads (docs/specs/cloud-app.md section 3.10): `vps.stagingSuffix` and the
// VPS's public hostname (`checklist.vpsPublicHostname`, whose A record is the publish target).
//
// The `localai` settings namespace itself is registered by a later issue (CLOUD-12). Until then this reads the stored
// system settings row (`value.localai`), nested (`{ vps: { stagingSuffix } }`) or dotted (`{ 'vps.stagingSuffix': … }`),
// like the wizard's reader. A suffix that is not valid is NOT replaced by the default: the answer says it is invalid, so
// a publish is refused (the suffix goes into DNS names, nginx files and certificate requests).
import { Injectable } from '@nestjs/common';

import { PrismaService } from '../../prisma/prisma.service';
import { DEFAULT_VPS_PUBLIC_HOSTNAME, wizardSettingsFrom } from '../wizard/wizard-settings';
import { isStagingSuffix } from './vps-host';

export interface VpsSettings {
  /** `""` in production. */
  stagingSuffix: string;
  /** False when a stored suffix is not valid; `stagingSuffix` is then `""` and nothing may be published. */
  stagingSuffixValid: boolean;
  vpsPublicHostname: string;
}

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);

/** Pure: the stored `localai` object (anything) to the stack-agent's settings. */
export function vpsSettingsFrom(localai: unknown): VpsSettings {
  const vpsPublicHostname = isRecord(localai) ? wizardSettingsFrom(localai).vpsPublicHostname : DEFAULT_VPS_PUBLIC_HOSTNAME;
  if (!isRecord(localai)) return { stagingSuffix: '', stagingSuffixValid: true, vpsPublicHostname };
  const nested = isRecord(localai.vps) ? localai.vps.stagingSuffix : undefined;
  const raw = nested ?? localai['vps.stagingSuffix'];
  if (raw === undefined || raw === null) return { stagingSuffix: '', stagingSuffixValid: true, vpsPublicHostname };
  if (typeof raw === 'string' && isStagingSuffix(raw)) return { stagingSuffix: raw, stagingSuffixValid: true, vpsPublicHostname };
  return { stagingSuffix: '', stagingSuffixValid: false, vpsPublicHostname };
}

@Injectable()
export class VpsSettingsService {
  constructor(private readonly prisma: PrismaService) {}

  async read(): Promise<VpsSettings> {
    const row = await this.prisma.systemSettings.findUnique({ where: { key: 'global' } });
    return vpsSettingsFrom(isRecord(row?.value) ? row.value.localai : undefined);
  }
}
