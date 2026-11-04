// A small catalog and an in-memory `CatalogApi` for the Catalog tests. Nothing here is a real person, host or key: the
// approver is "Person A", the revision is a dummy 40-hex value, the codes are registered in docs/specs/doctor-codes.md.
import { ApiError } from '@marinoscar/platform-web/core';

import type {
  ApproveResponse,
  CatalogApi,
  CatalogFile,
  CatalogStatus,
  CatalogVersionDetail,
  CatalogVersionSummary,
  CreateDraftBody,
  LintProblem,
} from '../api/catalog';

export const CAT_NOW = new Date('2026-11-03T12:00:00.000Z');

export const GOOD_REVISION = 'a'.repeat(40);

export const MODELS_YAML = [
  'schema_version: 1',
  'models:',
  '  - id: model-one',
  '    source:',
  `      revision: ${GOOD_REVISION}`,
  '  - id: model-two',
  '    source:',
  `      revision: ${GOOD_REVISION}`,
  '',
].join('\n');

export const BAD_MODELS_YAML = MODELS_YAML.replace(`revision: ${GOOD_REVISION}`, 'revision: main');

export const PROBLEM: LintProblem = {
  code: 'LAI-CAT-011',
  file: 'models.yaml',
  path: 'models[0].source.revision',
  message: 'must be a 40-hex commit',
};

const sha = (text: string) => `sha-${text.length}-${text.slice(0, 12)}`;

export function files(version: string, models = MODELS_YAML): CatalogFile[] {
  const entries = [
    { path: 'CATALOG_VERSION', content: `${version}\n` },
    { path: 'models.yaml', content: models },
    { path: 'ark.yaml', content: 'schema_version: 1\n' },
    { path: 'profiles/small-8gb.yaml', content: 'id: small-8gb\n' },
  ];
  return entries.map((entry) => ({ ...entry, sha256: sha(entry.content) }));
}

export function summary(version: string, status: CatalogStatus, over: Partial<CatalogVersionSummary> = {}): CatalogVersionSummary {
  return {
    id: `id-${version}`,
    version,
    source: 'github_release',
    sourceRef: `catalog-${version}`,
    status,
    bundleSha256: `bundle-${version}`,
    lintedAt: '2026-11-03T08:00:00.000Z',
    problemCount: 0,
    lintError: null,
    notes: null,
    createdById: null,
    createdAt: '2026-11-03T07:00:00.000Z',
    approvedById: status === 'approved' ? 'user-1' : null,
    approvedByName: status === 'approved' ? 'Person A' : null,
    approvedAt: status === 'approved' ? '2026-11-03T10:00:00.000Z' : null,
    ...over,
  };
}

/** The wireframe's three rows: a draft with 2 errors, the approved one, a superseded one. */
export function wireframeVersions(): CatalogVersionSummary[] {
  return [
    summary('2026.11.03-1', 'invalid', { source: 'editor', sourceRef: '2026.11.02-1', problemCount: 2 }),
    summary('2026.11.02-1', 'approved'),
    summary('2026.10.28-2', 'superseded'),
  ];
}

export interface FakeCatalog {
  api: CatalogApi;
  rows: Map<string, CatalogVersionDetail>;
  calls: {
    list: ReturnType<typeof vi.fn>;
    get: ReturnType<typeof vi.fn>;
    importRelease: ReturnType<typeof vi.fn>;
    createDraft: ReturnType<typeof vi.fn>;
    lint: ReturnType<typeof vi.fn>;
    approve: ReturnType<typeof vi.fn>;
    refreshNode: ReturnType<typeof vi.fn>;
  };
}

/**
 * An in-memory catalog behind the `CatalogApi` shape. `lintVerdict` decides what a saved draft's lint says (the real
 * one runs `aictl` in a job; here it lands when `settle()` is called, or at once with `autoLint`).
 */
export function fakeCatalog(
  initial: CatalogVersionSummary[] = wireframeVersions(),
  options: { filesOf?: (version: string) => CatalogFile[]; autoLint?: boolean; lintProblems?: (body: CreateDraftBody) => LintProblem[] } = {},
): FakeCatalog & { settle(version: string): void } {
  const rows = new Map<string, CatalogVersionDetail>();
  for (const item of initial) {
    rows.set(item.version, {
      ...item,
      files: options.filesOf ? options.filesOf(item.version) : files(item.version, item.status === 'invalid' ? BAD_MODELS_YAML : MODELS_YAML),
      lintResult: item.status === 'invalid' ? { valid: false, problems: [PROBLEM, { ...PROBLEM, path: 'models[1].source.revision' }] } : item.lintedAt ? { valid: true, problems: [] } : null,
    });
  }
  const pendingProblems = new Map<string, LintProblem[]>();

  const settle = (version: string) => {
    const row = rows.get(version);
    if (!row) return;
    const problems = pendingProblems.get(version) ?? [];
    row.lintedAt = '2026-11-03T12:00:05.000Z';
    row.status = problems.length ? 'invalid' : 'valid';
    row.problemCount = problems.length;
    row.lintResult = { valid: problems.length === 0, problems };
  };

  const calls = {
    list: vi.fn(async () => [...rows.values()].map(({ files: _f, lintResult: _l, diff: _d, ...rest }) => ({ ...rest })).sort((a, b) => (a.version < b.version ? 1 : -1))),
    get: vi.fn(async (version: string, diff?: 'approved') => {
      const row = rows.get(version);
      if (!row) throw new ApiError('Catalog not found', 404, undefined, { reason: 'version_not_found' });
      const detail: CatalogVersionDetail = structuredClone(row);
      if (diff === 'approved') {
        const approved = [...rows.values()].find((item) => item.status === 'approved');
        detail.diff = { against: approved?.version ?? null, files: [] };
      }
      return detail;
    }),
    importRelease: vi.fn(async (tag: string) => {
      const version = tag.replace('catalog-', '');
      if (rows.has(version)) throw new ApiError('Catalog already exists', 409, undefined, { reason: 'version_exists' });
      return { tag, version, jobId: 'job-1' };
    }),
    createDraft: vi.fn(async (body: CreateDraftBody) => {
      if (rows.has(body.version)) throw new ApiError('Catalog already exists', 409, undefined, { reason: 'version_exists' });
      const created = summary(body.version, 'draft', { source: 'editor', sourceRef: body.baseVersion ?? null, lintedAt: null, notes: body.notes ?? null });
      const problems = options.lintProblems ? options.lintProblems(body) : [];
      const fileList: CatalogFile[] = [{ path: 'CATALOG_VERSION', content: `${body.version}\n`, sha256: sha(body.version) }, ...body.files.map((file) => ({ ...file, sha256: sha(file.content) }))];
      rows.set(body.version, { ...created, files: fileList, lintResult: null });
      pendingProblems.set(body.version, problems);
      if (options.autoLint) settle(body.version);
      return created;
    }),
    lint: vi.fn(async (version: string) => ({ version, jobId: 'job-lint' })),
    approve: vi.fn(async (version: string, notifyNodes: boolean): Promise<ApproveResponse> => {
      const row = rows.get(version);
      if (!row || row.status !== 'valid') throw new ApiError(`Catalog ${version} is ${row?.status}; only a valid catalog can be approved.`, 409, undefined, { reason: 'not_valid' });
      const previous = [...rows.values()].find((item) => item.status === 'approved');
      if (previous) previous.status = 'superseded';
      row.status = 'approved';
      row.approvedAt = '2026-11-03T12:10:00.000Z';
      row.approvedByName = 'Person A';
      const { files: _f, lintResult: _l, diff: _d, ...rest } = row;
      return { version: rest, supersedes: previous?.version ?? null, notified: notifyNodes ? { queued: ['ai-n1', 'ai-n2'], alreadyQueued: [], skipped: 1 } : null };
    }),
    refreshNode: vi.fn(async (_nodeId: string) => undefined),
  };
  return { api: calls as unknown as CatalogApi, rows, calls, settle };
}
