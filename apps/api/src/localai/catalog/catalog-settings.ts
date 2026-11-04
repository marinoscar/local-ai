// The `localai` settings the catalog slice reads (docs/specs/cloud-app.md section 3.10): `catalog.githubRepo`.
//
// The `localai` settings namespace and its API are a later issue (GW-02). Until it registers the namespace this
// reader looks in the stored system settings row, `value.localai`, accepting both the nested form
// (`{ catalog: { githubRepo } }`) and the dotted key (`{ 'catalog.githubRepo': ... }`), and falls back to the
// default. A stored value that is not `<owner>/<repo>` is ignored (it will be put in a URL).
import { Injectable } from '@nestjs/common';

import { PrismaService } from '../../prisma/prisma.service';
import { DEFAULT_CATALOG_GITHUB_REPO } from './catalog.constants';

const REPO_RE = /^[A-Za-z0-9_.-]{1,100}\/[A-Za-z0-9_.-]{1,100}$/;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

/** The repository named by a stored `localai` settings object, or the default. Pure, for tests. */
export function githubRepoFromSettings(localai: unknown): string {
  if (!isRecord(localai)) return DEFAULT_CATALOG_GITHUB_REPO;
  const nested = isRecord(localai.catalog) ? localai.catalog.githubRepo : undefined;
  const candidate = nested ?? localai['catalog.githubRepo'];
  return typeof candidate === 'string' && REPO_RE.test(candidate) && !candidate.includes('..') ? candidate : DEFAULT_CATALOG_GITHUB_REPO;
}

@Injectable()
export class CatalogSettings {
  constructor(private readonly prisma: PrismaService) {}

  async githubRepo(): Promise<string> {
    const row = await this.prisma.systemSettings.findUnique({ where: { key: 'global' } });
    return githubRepoFromSettings(isRecord(row?.value) ? row.value.localai : undefined);
  }
}
