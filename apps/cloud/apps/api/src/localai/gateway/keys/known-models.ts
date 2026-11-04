// The model ids and aliases a key's `allowedModels` may name, known at write time (docs/specs/gateway.md section 3.8):
// the ids of the approved catalog plus every id and alias of the model policies. A key that names anything else is a 400,
// so a typo cannot create a key that silently allows nothing.
import { Injectable } from '@nestjs/common';

import { PrismaService } from '../../../prisma/prisma.service';

export const GATEWAY_KNOWN_MODELS = Symbol('GATEWAY_KNOWN_MODELS');

export interface KnownModels {
  /** Every id and alias a key may name. */
  names(): Promise<ReadonlySet<string>>;
}

const MODEL_ID = /^\s*-\s+id:\s*["']?([a-z0-9][a-z0-9.\-]{2,63})["']?\s*(?:#.*)?$/;
const TOP_LEVEL_KEY = /^[A-Za-z_][\w-]*:/;

/** The ids under `models:` of a catalog's `models.yaml` text. Forgiving: the catalog was linted before it was approved. */
export function modelIdsFromYaml(yaml: string): string[] {
  const ids: string[] = [];
  let inModels = false;
  for (const line of yaml.split(/\r?\n/)) {
    if (TOP_LEVEL_KEY.test(line)) {
      inModels = line.startsWith('models:');
      continue;
    }
    if (!inModels) continue;
    const id = MODEL_ID.exec(line);
    if (id) ids.push(id[1]!);
  }
  return ids;
}

/** The ids in a stored catalog's `files` JSON (an array of `{ path, content }`). */
export function modelIdsFromFiles(files: unknown): string[] {
  if (!Array.isArray(files)) return [];
  for (const file of files) {
    if (file && typeof file === 'object' && (file as { path?: unknown }).path === 'models.yaml' && typeof (file as { content?: unknown }).content === 'string') {
      return modelIdsFromYaml((file as { content: string }).content);
    }
  }
  return [];
}

@Injectable()
export class CatalogKnownModels implements KnownModels {
  constructor(private readonly prisma: PrismaService) {}

  async names(): Promise<ReadonlySet<string>> {
    const [approved, policies] = await Promise.all([
      this.prisma.localaiCatalogVersion.findFirst({ where: { status: 'approved' }, select: { files: true } }),
      this.prisma.localaiModelPolicy.findMany({ select: { modelId: true, aliases: true } }),
    ]);
    const names = new Set<string>(modelIdsFromFiles(approved?.files));
    for (const policy of policies) {
      names.add(policy.modelId);
      for (const alias of policy.aliases) names.add(alias);
    }
    return names;
  }
}
