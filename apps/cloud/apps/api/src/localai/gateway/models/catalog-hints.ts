// What `/v1/models` and the model resolution read from the APPROVED catalog (docs/specs/gateway.md section 3.4 and 4.4,
// catalog.md section 2): the model ids that exist, and per model the display name, roles, `client_hints` and the
// configured context. `models.yaml` is parsed once per approved version and cached; a cheap head query (version and
// approval time, no file content) decides, at most every few seconds, whether the cached parse is still the approved one.
//
// The catalog was linted by the real `aictl` before it could be approved, so the layout is the documented one. The
// parser is still defensive: a model it cannot read is skipped, and a file it cannot parse at all gives an empty model
// set (nothing is listed, nothing resolves) with a warning, never an exception on the request path.
import { Inject, Injectable, Logger } from '@nestjs/common';
import { parse } from 'yaml';

import { PrismaService } from '../../../prisma/prisma.service';
import { LOCALAI_CLOCK, type LocalaiClock } from '../../commands/command-events';

/** The catalog's model id rule (catalog.md section 2). */
const MODEL_ID = /^[a-z0-9][a-z0-9.-]{2,63}$/;

export interface CatalogModelHints {
  id: string;
  displayName: string;
  roles: readonly string[];
  /** `client_hints.max_output_tokens`; null when the catalog gives none. */
  maxOutputTokens: number | null;
  /** `client_hints.reasoning`; false when absent. */
  reasoning: boolean;
  /** `llama.ctx`, the context the model is configured for; null for a model without a `llama` block. */
  ctx: number | null;
}

/** The approved catalog as the gateway needs it. */
export interface ApprovedCatalog {
  version: string;
  approvedAt: Date;
  /** `approvedAt` in Unix seconds: the `created` of every listed model, the same on every call of this version. */
  createdSeconds: number;
  models: ReadonlyMap<string, CatalogModelHints>;
}

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);
const positiveInteger = (value: unknown): number | null => (typeof value === 'number' && Number.isSafeInteger(value) && value > 0 ? value : null);

/** The models of a `models.yaml` text by id. Pure; never throws. */
export function parseModelsYaml(text: string): Map<string, CatalogModelHints> {
  const models = new Map<string, CatalogModelHints>();
  let document: unknown;
  try {
    document = parse(text);
  } catch {
    return models;
  }
  const list = isRecord(document) ? document.models : undefined;
  if (!Array.isArray(list)) return models;
  for (const entry of list) {
    if (!isRecord(entry) || typeof entry.id !== 'string' || !MODEL_ID.test(entry.id) || models.has(entry.id)) continue;
    const hints = isRecord(entry.client_hints) ? entry.client_hints : {};
    const llama = isRecord(entry.llama) ? entry.llama : {};
    models.set(entry.id, {
      id: entry.id,
      displayName: typeof entry.display_name === 'string' && entry.display_name.trim() !== '' ? entry.display_name : entry.id,
      roles: Array.isArray(entry.roles) ? entry.roles.filter((role): role is string => typeof role === 'string') : [],
      maxOutputTokens: positiveInteger(hints.max_output_tokens),
      reasoning: hints.reasoning === true,
      ctx: positiveInteger(llama.ctx),
    });
  }
  return models;
}

/** The newest approval, without the files. */
export interface ApprovedCatalogHead {
  version: string;
  approvedAt: Date;
}

/** Where the approved catalog is read from; a port so the unit specs use a fake and the database tier the real query. */
export interface ApprovedCatalogStore {
  /** The approved version (at most one row is approved), or null when none is. */
  head(): Promise<ApprovedCatalogHead | null>;
  /** The text of that version's `models.yaml`, or null when it has none. */
  modelsYaml(version: string): Promise<string | null>;
}

/** Injection token of the {@link ApprovedCatalogStore}. */
export const APPROVED_CATALOG_STORE = Symbol('LOCALAI_GATEWAY_APPROVED_CATALOG_STORE');

@Injectable()
export class PrismaApprovedCatalogStore implements ApprovedCatalogStore {
  constructor(private readonly prisma: PrismaService) {}

  async head(): Promise<ApprovedCatalogHead | null> {
    const row = await this.prisma.localaiCatalogVersion.findFirst({
      where: { status: 'approved' },
      select: { version: true, approvedAt: true, createdAt: true },
    });
    return row ? { version: row.version, approvedAt: row.approvedAt ?? row.createdAt } : null;
  }

  async modelsYaml(version: string): Promise<string | null> {
    const row = await this.prisma.localaiCatalogVersion.findUnique({ where: { version }, select: { files: true } });
    if (!row || !Array.isArray(row.files)) return null;
    for (const file of row.files) {
      if (isRecord(file) && file.path === 'models.yaml' && typeof file.content === 'string') return file.content;
    }
    return null;
  }
}

/** How long the answer of the head query is trusted. An approval shows in `/v1/models` within this. */
export const CATALOG_HEAD_TTL_MS = 5_000;

/** The approved catalog, parsed once per version. */
@Injectable()
export class ApprovedCatalogReader {
  private readonly logger = new Logger(ApprovedCatalogReader.name);
  private cached: { catalog: ApprovedCatalog | null; checkedAt: number } | null = null;
  private inflight: Promise<ApprovedCatalog | null> | null = null;

  constructor(
    @Inject(APPROVED_CATALOG_STORE) private readonly store: ApprovedCatalogStore,
    @Inject(LOCALAI_CLOCK) private readonly clock: LocalaiClock,
  ) {}

  /** The approved catalog, or null when none is approved. A failing read serves the last good answer, or throws when there is none. */
  async current(): Promise<ApprovedCatalog | null> {
    const now = this.clock.now().getTime();
    if (this.cached && now - this.cached.checkedAt < CATALOG_HEAD_TTL_MS) return this.cached.catalog;
    this.inflight ??= this.refresh(now).finally(() => {
      this.inflight = null;
    });
    return this.inflight;
  }

  /** Forgets the cached answer; the next call asks the store. */
  invalidate(): void {
    this.cached = null;
  }

  private async refresh(now: number): Promise<ApprovedCatalog | null> {
    try {
      const head = await this.store.head();
      let catalog: ApprovedCatalog | null = null;
      if (head) {
        const previous = this.cached?.catalog;
        if (previous && previous.version === head.version && previous.approvedAt.getTime() === head.approvedAt.getTime()) {
          catalog = previous;
        } else {
          const yaml = await this.store.modelsYaml(head.version);
          const models = parseModelsYaml(yaml ?? '');
          if (models.size === 0) this.logger.warn(`the approved catalog ${head.version} has no readable models`);
          catalog = { version: head.version, approvedAt: head.approvedAt, createdSeconds: Math.floor(head.approvedAt.getTime() / 1000), models };
        }
      }
      this.cached = { catalog, checkedAt: now };
      return catalog;
    } catch (error) {
      // Error NAME only: a driver message could carry connection details.
      this.logger.warn(`could not read the approved catalog${this.cached ? ', serving the previous one' : ''}: ${error instanceof Error ? error.name : 'unknown error'}`);
      if (this.cached) return this.cached.catalog;
      throw error;
    }
  }
}
