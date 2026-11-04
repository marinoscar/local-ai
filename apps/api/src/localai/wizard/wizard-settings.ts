// The `localai` settings the wizard reads (docs/specs/cloud-app.md section 3.10): `agent.latestVersion`,
// `checklist.vpsPublicHostname` and `catalog.githubRepo`.
//
// The namespace itself is registered by a later issue. Until it exists this reads the stored system settings row
// (`value.localai`), accepting both the nested form (`{ agent: { latestVersion } }`) and the dotted key
// (`{ 'agent.latestVersion': ... }`), like the catalog slice's reader, and falls back to the documented defaults.
// Each value is validated because it ends up in a URL, a command or a DNS query.
import { Injectable } from '@nestjs/common';

import { DEFAULT_CATALOG_GITHUB_REPO } from '../catalog/catalog.constants';
import { githubRepoFromSettings } from '../catalog/catalog-settings';
import { PrismaService } from '../../prisma/prisma.service';
import { PUBLIC_HOST_RE } from './node-id';

export const DEFAULT_VPS_PUBLIC_HOSTNAME = 'ai.marin.cr';

export interface WizardSettings {
  agentLatestVersion: string | null;
  vpsPublicHostname: string;
  githubRepo: string;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function pick(localai: Record<string, unknown>, group: string, key: string): unknown {
  const nested = isRecord(localai[group]) ? (localai[group] as Record<string, unknown>)[key] : undefined;
  return nested ?? localai[`${group}.${key}`];
}

/** Pure: the stored `localai` object (anything) to the wizard's settings. Exported for the unit test. */
export function wizardSettingsFrom(localai: unknown): WizardSettings {
  if (!isRecord(localai)) return { agentLatestVersion: null, vpsPublicHostname: DEFAULT_VPS_PUBLIC_HOSTNAME, githubRepo: DEFAULT_CATALOG_GITHUB_REPO };
  const version = pick(localai, 'agent', 'latestVersion');
  const host = pick(localai, 'checklist', 'vpsPublicHostname');
  return {
    agentLatestVersion: typeof version === 'string' && /^\d+\.\d+\.\d+$/.test(version.trim()) ? version.trim() : null,
    vpsPublicHostname: typeof host === 'string' && PUBLIC_HOST_RE.test(host.trim().toLowerCase()) ? host.trim().toLowerCase() : DEFAULT_VPS_PUBLIC_HOSTNAME,
    githubRepo: githubRepoFromSettings(localai),
  };
}

@Injectable()
export class WizardSettingsService {
  constructor(private readonly prisma: PrismaService) {}

  async read(): Promise<WizardSettings> {
    const row = await this.prisma.systemSettings.findUnique({ where: { key: 'global' } });
    return wizardSettingsFrom(isRecord(row?.value) ? row.value.localai : undefined);
  }
}
