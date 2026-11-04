// The catalog state machine over an in-memory stand-in for the one table it uses (no PostgreSQL). The real SQL
// (the partial unique index, the transaction, the concurrent approvals) is proven by `test/localai/catalog.db.spec.ts`.
import { randomUUID } from 'node:crypto';

import { buildCatalogArchive, buildTarGz, repoCatalogFiles } from '../../../test/localai/helpers/tar-builder';
import { ArchiveFetchError } from './archive-fetcher';
import { CatalogService } from './catalog.service';
import type { CatalogLinter, LintOutcome } from './catalog-linter';
import { toCatalogFiles, bundleSha256 } from './bundle-hash';
import type { CatalogFile } from './catalog.constants';

type Row = Record<string, any>; // eslint-disable-line @typescript-eslint/no-explicit-any

class FakeTable {
  rows: Row[] = [];
  private matches(row: Row, where: Row = {}): boolean {
    return Object.entries(where).every(([key, expected]) => {
      if (expected && typeof expected === 'object' && 'in' in expected) return (expected.in as unknown[]).includes(row[key]);
      return row[key] === expected;
    });
  }
  findUnique = async ({ where }: { where: Row }) => this.rows.find((row) => this.matches(row, where)) ?? null;
  findUniqueOrThrow = async ({ where }: { where: Row }) => {
    const row = await this.findUnique({ where });
    if (!row) throw new Error('not found');
    return row;
  };
  findFirst = async ({ where }: { where?: Row } = {}) => this.rows.find((row) => this.matches(row, where)) ?? null;
  findMany = async ({ omit }: { select?: Row; omit?: Row } = {}) =>
    this.rows.map((row) => (omit?.files ? Object.fromEntries(Object.entries(row).filter(([key]) => key !== 'files')) : row));
  create = async ({ data }: { data: Row }) => {
    if (this.rows.some((row) => row.version === data.version)) throw Object.assign(new Error('unique'), { code: 'P2002' });
    const row = { id: randomUUID(), lintResult: null, lintedAt: null, approvedById: null, approvedAt: null, createdAt: new Date(), ...data };
    this.rows.push(row);
    return row;
  };
  updateMany = async ({ where, data }: { where: Row; data: Row }) => {
    const hit = this.rows.filter((row) => this.matches(row, where));
    for (const row of hit) Object.assign(row, data);
    return { count: hit.length };
  };
}

function build(options: { linter?: CatalogLinter; fetch?: (url: string) => Promise<Buffer>; refresh?: () => Promise<unknown> } = {}) {
  const table = new FakeTable();
  const prisma = { localaiCatalogVersion: table, $transaction: async (fn: (tx: unknown) => unknown) => fn(prisma) };
  const jobs = { enqueue: jest.fn(async (input: Row) => ({ id: `job-${input.type}-${input.subjectId}` })) };
  const audits: Row[] = [];
  const audit = { record: jest.fn(async (event: Row) => void audits.push(event)) };
  const requests: string[] = [];
  const fetcher = {
    fetch: jest.fn(async (url: string) => {
      requests.push(url);
      return options.fetch ? options.fetch(url) : buildCatalogArchive();
    }),
  };
  const linter: CatalogLinter = options.linter ?? { lint: async () => ({ status: 'valid', result: { valid: true, problems: [], exitCode: 0 } }) };
  const refresh = { queueForEligibleNodes: jest.fn(options.refresh ?? (async () => ({ queued: ['ai-n2'], alreadyQueued: [], skipped: 1 }))) };
  const settings = { githubRepo: async () => 'org-alpha/lab-catalog' };
  const service = new CatalogService(prisma as never, jobs as never, settings as never, refresh as never, audit as never, fetcher as never, linter);
  return { service, table, jobs, audits, requests, refresh, fetcher };
}

function draftRow(version: string, status: string, over: Row = {}): Row {
  const files: CatalogFile[] = toCatalogFiles([
    { path: 'CATALOG_VERSION', content: `${version}\n` },
    { path: 'models.yaml', content: `schema_version: 1\n# ${version}\n` },
  ]);
  return {
    id: randomUUID(),
    version,
    source: 'editor',
    sourceRef: null,
    status,
    files,
    bundleSha256: bundleSha256(files),
    lintResult: null,
    lintedAt: null,
    notes: null,
    createdById: null,
    approvedById: null,
    approvedAt: null,
    createdAt: new Date('2026-11-01T00:00:00Z'),
    ...over,
  };
}

const ACTOR = '00000000-0000-4000-8000-000000000001';
const editorBody = (version: string, over: Row = {}) => ({
  version,
  source: 'editor' as const,
  files: [
    { path: 'models.yaml', content: 'schema_version: 1\n' },
    { path: 'ark.yaml', content: 'schema_version: 1\n' },
  ],
  ...over,
});

async function status(promise: Promise<unknown>): Promise<number> {
  try {
    await promise;
  } catch (error) {
    return (error as { getStatus(): number }).getStatus();
  }
  return 200;
}

describe('CatalogService', () => {
  describe('requestImport', () => {
    it('queues the import job for a catalog tag, keyed by the version', async () => {
      const { service, jobs } = build();
      expect(await service.requestImport('catalog-2026.11.02-1', ACTOR)).toEqual({
        tag: 'catalog-2026.11.02-1',
        version: '2026.11.02-1',
        jobId: 'job-localai.catalog.import-2026.11.02-1',
      });
      expect(jobs.enqueue).toHaveBeenCalledWith(
        expect.objectContaining({ type: 'localai.catalog.import', subjectType: 'localai_catalog', subjectId: '2026.11.02-1', orgId: null, payload: { tag: 'catalog-2026.11.02-1', actorUserId: ACTOR } }),
      );
    });

    it('answers 409 for a version that already exists and 400 for a tag that is not catalog-<version>', async () => {
      const { service, table, jobs } = build();
      table.rows.push(draftRow('2026.11.02-1', 'approved'));
      expect(await status(service.requestImport('catalog-2026.11.02-1', ACTOR))).toBe(409);
      expect(await status(service.requestImport('catalog-stable', ACTOR))).toBe(400);
      expect(jobs.enqueue).not.toHaveBeenCalled();
    });
  });

  describe('importFromArchive (the import job)', () => {
    it('stores a draft with the right digests, audits it and queues the lint', async () => {
      const sample = repoCatalogFiles();
      const version = sample.get('CATALOG_VERSION')!.toString('utf8').trim();
      const { service, table, jobs, audits, requests } = build();
      const summary = await service.importFromArchive(`catalog-${version}`, ACTOR);
      expect(requests).toEqual([`https://github.com/org-alpha/lab-catalog/archive/refs/tags/catalog-${version}.tar.gz`]);
      expect(summary).toMatchObject({ version, source: 'github_release', sourceRef: `catalog-${version}`, status: 'draft' });
      const row = table.rows[0];
      expect((row.files as CatalogFile[]).map((file) => file.path)).toEqual(['CATALOG_VERSION', 'models.yaml', 'ark.yaml', ...[...sample.keys()].filter((p) => p.startsWith('profiles/')).sort()]);
      expect(row.bundleSha256).toBe(bundleSha256(row.files));
      expect(row.createdById).toBe(ACTOR);
      expect(jobs.enqueue).toHaveBeenCalledWith(expect.objectContaining({ type: 'localai.catalog.lint', subjectId: version, reason: 'upload' }));
      expect(audits).toEqual([
        { action: 'localai.catalog.imported', actorUserId: ACTOR, targetType: 'localai_catalog', targetId: version, meta: { source: 'github_release', sourceRef: `catalog-${version}` } },
      ]);
    });

    it('refuses an archive whose CATALOG_VERSION disagrees with the tag', async () => {
      const { service, table } = build({ fetch: async () => buildCatalogArchive({ override: { CATALOG_VERSION: '2026.01.01-1\n' } }) });
      await expect(service.importFromArchive('catalog-2026.11.02-1', ACTOR)).rejects.toThrow(/version_mismatch/);
      expect(table.rows).toHaveLength(0);
    });

    it.each([
      ['a symlink under catalog/', { extra: [{ path: 'local-ai-catalog-fixture/catalog/profiles/evil.yaml', type: 'symlink' as const, linkTarget: '/etc/passwd' }] }, /link_entry/],
      ['an absolute path', { extra: [{ path: '/etc/cron.d/x', content: 'x' }] }, /unsafe_path/],
      ['a path traversal', { extra: [{ path: 'local-ai-catalog-fixture/catalog/../../x', content: 'x' }] }, /unsafe_path/],
    ])('refuses %s and stores nothing', async (_name, options, message) => {
      const { service, table, jobs } = build({ fetch: async () => buildCatalogArchive(options) });
      await expect(service.importFromArchive('catalog-2026.11.02-1', ACTOR)).rejects.toThrow(message);
      expect(table.rows).toHaveLength(0);
      expect(jobs.enqueue).not.toHaveBeenCalled();
    });

    it('reports a failed download with LAI-CLD-013 and no detail from the network', async () => {
      const { service } = build({
        fetch: async () => {
          throw new ArchiveFetchError('not_found', 'GitHub has no such release tag.');
        },
      });
      await expect(service.importFromArchive('catalog-2026.11.02-1', ACTOR)).rejects.toThrow(/LAI-CLD-013.*not_found/);
    });

    it('refuses a tag whose version already exists, but treats a re-run of the same import as done', async () => {
      const { service, table, jobs } = build();
      table.rows.push(draftRow('2026.11.02-1', 'draft', { source: 'editor' }));
      await expect(service.importFromArchive('catalog-2026.11.02-1', ACTOR)).rejects.toThrow(/already exists/);

      table.rows[0] = draftRow('2026.11.02-1', 'draft', { source: 'github_release', sourceRef: 'catalog-2026.11.02-1' });
      await expect(service.importFromArchive('catalog-2026.11.02-1', ACTOR)).resolves.toMatchObject({ version: '2026.11.02-1' });
      expect(jobs.enqueue).toHaveBeenCalledWith(expect.objectContaining({ type: 'localai.catalog.lint' }));
      expect(table.rows).toHaveLength(1);
    });

    it('refuses to download when the repository setting is not a GitHub <owner>/<repo>', async () => {
      const { service, fetcher } = build();
      (service as unknown as { settings: { githubRepo(): Promise<string> } }).settings = { githubRepo: async () => '../etc' };
      await expect(service.importFromArchive('catalog-2026.11.02-1', ACTOR)).rejects.toThrow(/catalog\.githubRepo/);
      expect(fetcher.fetch).not.toHaveBeenCalled();
    });

    it('rejects an archive of the wrong layout with a plain message', async () => {
      const { service } = build({ fetch: async () => buildTarGz([{ path: 'top/README.md', content: 'x' }]) });
      await expect(service.importFromArchive('catalog-2026.11.02-1', ACTOR)).rejects.toThrow(/no_catalog/);
    });
  });

  describe('createDraft (editor and upload)', () => {
    it('creates a draft, rewrites CATALOG_VERSION, records the base and queues the lint', async () => {
      const { service, table, jobs, audits } = build();
      table.rows.push(draftRow('2026.11.02-1', 'approved'));
      const summary = await service.createDraft(editorBody('2026.11.03-1', { baseVersion: '2026.11.02-1', notes: 'raise ctx', source: 'upload' }), ACTOR);
      expect(summary).toMatchObject({ version: '2026.11.03-1', source: 'upload', sourceRef: '2026.11.02-1', status: 'draft', notes: 'raise ctx' });
      const stored = table.rows.find((row) => row.version === '2026.11.03-1')!;
      expect((stored.files as CatalogFile[]).find((file) => file.path === 'CATALOG_VERSION')?.content).toBe('2026.11.03-1\n');
      expect(jobs.enqueue).toHaveBeenCalledWith(expect.objectContaining({ type: 'localai.catalog.lint', subjectId: '2026.11.03-1' }));
      expect(audits[0]).toMatchObject({ action: 'localai.catalog.imported', meta: { source: 'upload', sourceRef: '2026.11.02-1' } });
    });

    it('answers 400 when the version is not greater than every existing one (numeric N)', async () => {
      const { service, table } = build();
      table.rows.push(draftRow('2026.11.02-9', 'superseded'), draftRow('2026.11.03-1', 'draft'));
      for (const version of ['2026.11.03-1', '2026.11.02-10', '2026.10.01-5']) expect(await status(service.createDraft(editorBody(version), ACTOR))).toBe(400);
      expect(await status(service.createDraft(editorBody('2026.11.03-2'), ACTOR))).toBe(200);
    });

    it('answers 400 for a disallowed or duplicated path and 404 for an unknown base version', async () => {
      const { service, table } = build();
      expect(await status(service.createDraft(editorBody('2026.11.03-1', { files: [{ path: 'README.md', content: 'x' }] }), ACTOR))).toBe(400);
      expect(await status(service.createDraft(editorBody('2026.11.03-1', { files: [{ path: '../models.yaml', content: 'x' }] }), ACTOR))).toBe(400);
      expect(
        await status(service.createDraft(editorBody('2026.11.03-1', { files: [{ path: 'models.yaml', content: 'a' }, { path: 'models.yaml', content: 'b' }] }), ACTOR)),
      ).toBe(400);
      expect(await status(service.createDraft(editorBody('2026.11.03-1', { baseVersion: '2026.10.01-1' }), ACTOR))).toBe(404);
      expect(table.rows).toHaveLength(0);
    });

    it('answers 413 above 2 MB, counting bytes', async () => {
      const { service, table } = build();
      const big = 'a'.repeat(2 * 1024 * 1024 + 1);
      expect(await status(service.createDraft(editorBody('2026.11.03-1', { files: [{ path: 'models.yaml', content: big }] }), ACTOR))).toBe(413);
      expect(await status(service.createDraft(editorBody('2026.11.03-1', { files: [{ path: 'models.yaml', content: 'é'.repeat(1024 * 1024 + 1) }] }), ACTOR))).toBe(413);
      expect(table.rows).toHaveLength(0);
    });
  });

  describe('runLint (the lint job)', () => {
    const lintWith = (outcome: LintOutcome) => build({ linter: { lint: async () => outcome } });

    it('maps a clean run to valid and audits it', async () => {
      const { service, table, audits } = lintWith({ status: 'valid', result: { valid: true, problems: [], exitCode: 0 } });
      table.rows.push(draftRow('2026.11.03-1', 'draft', { source: 'github_release', sourceRef: 'catalog-2026.11.03-1' }));
      await service.runLint('2026.11.03-1', ACTOR);
      expect(table.rows[0]).toMatchObject({ status: 'valid', lintResult: { valid: true } });
      expect(table.rows[0].lintedAt).toBeInstanceOf(Date);
      expect(audits).toEqual([
        { action: 'localai.catalog.linted', actorUserId: ACTOR, targetType: 'localai_catalog', targetId: '2026.11.03-1', meta: { source: 'github_release', sourceRef: 'catalog-2026.11.03-1', valid: true } },
      ]);
    });

    it('maps problems to invalid and stores them', async () => {
      const problems = [{ code: 'LAI-CAT-011', file: 'models.yaml', path: 'models[0]', message: 'bad' }];
      const { service, table, audits } = lintWith({ status: 'invalid', result: { valid: false, problems, exitCode: 2 } });
      table.rows.push(draftRow('2026.11.03-1', 'draft'));
      await service.runLint('2026.11.03-1', null);
      expect(table.rows[0]).toMatchObject({ status: 'invalid', lintResult: { valid: false, problems } });
      expect(audits[0]).toMatchObject({ actorUserId: null, meta: { valid: false } });
    });

    it('keeps a version whose lint could not run as a draft with the error, and returns normally', async () => {
      const error = { code: 'LAI-CLD-033' as const, reason: 'aictl_missing' as const, message: 'not installed' };
      const { service, table } = lintWith({ status: 'draft', result: { error } });
      table.rows.push(draftRow('2026.11.03-1', 'valid'));
      await expect(service.runLint('2026.11.03-1', ACTOR)).resolves.toBeUndefined();
      expect(table.rows[0]).toMatchObject({ status: 'draft', lintResult: { error } });
    });

    it('never touches approved or superseded history, and ignores a vanished version', async () => {
      const calls: number[] = [];
      const { service, table } = build({ linter: { lint: async () => (calls.push(1), { status: 'invalid', result: { valid: false, problems: [] } }) } });
      table.rows.push(draftRow('2026.11.01-1', 'superseded'), draftRow('2026.11.02-1', 'approved'));
      await service.runLint('2026.11.01-1', ACTOR);
      await service.runLint('2026.11.02-1', ACTOR);
      await service.runLint('2026.12.31-1', ACTOR);
      expect(calls).toHaveLength(0);
      expect(table.rows.map((row) => row.status)).toEqual(['superseded', 'approved']);
    });

    it('does not overwrite an approval that landed while the linter ran', async () => {
      const { service, table, audits } = build();
      table.rows.push(draftRow('2026.11.03-1', 'valid'));
      (service as unknown as { linter: CatalogLinter }).linter = {
        lint: async () => {
          table.rows[0].status = 'approved';
          return { status: 'invalid', result: { valid: false, problems: [] } };
        },
      };
      await service.runLint('2026.11.03-1', ACTOR);
      expect(table.rows[0].status).toBe('approved');
      expect(audits).toHaveLength(0);
    });
  });

  describe('requestLint', () => {
    it('queues a re-lint for a draft, invalid or valid version and refuses history with 409', async () => {
      const { service, table, jobs } = build();
      table.rows.push(draftRow('2026.11.01-1', 'superseded'), draftRow('2026.11.02-1', 'approved'), draftRow('2026.11.03-1', 'invalid'));
      expect(await service.requestLint('2026.11.03-1', ACTOR)).toEqual({ version: '2026.11.03-1', jobId: 'job-localai.catalog.lint-2026.11.03-1' });
      expect(jobs.enqueue).toHaveBeenCalledWith(expect.objectContaining({ reason: 'rerun' }));
      expect(await status(service.requestLint('2026.11.01-1', ACTOR))).toBe(409);
      expect(await status(service.requestLint('2026.11.02-1', ACTOR))).toBe(409);
      expect(await status(service.requestLint('2099.01.01-1', ACTOR))).toBe(404);
      expect(await status(service.requestLint('not-a-version', ACTOR))).toBe(404);
    });
  });

  describe('approve', () => {
    it.each(['draft', 'invalid', 'approved', 'superseded'])('refuses a %s version with 409', async (current) => {
      const { service, table, refresh } = build();
      table.rows.push(draftRow('2026.11.03-1', current));
      expect(await status(service.approve('2026.11.03-1', { notifyNodes: true }, ACTOR))).toBe(409);
      expect(table.rows[0].status).toBe(current);
      expect(refresh.queueForEligibleNodes).not.toHaveBeenCalled();
    });

    it('approves a valid version, supersedes the previous approved one and records who and when', async () => {
      const { service, table, audits, refresh } = build();
      table.rows.push(draftRow('2026.11.02-1', 'approved'), draftRow('2026.11.03-1', 'valid', { source: 'editor', sourceRef: '2026.11.02-1' }));
      const result = await service.approve('2026.11.03-1', { notifyNodes: false }, ACTOR);
      expect(result.supersedes).toBe('2026.11.02-1');
      expect(result.notified).toBeNull();
      expect(table.rows.map((row) => [row.version, row.status])).toEqual([
        ['2026.11.02-1', 'superseded'],
        ['2026.11.03-1', 'approved'],
      ]);
      expect(table.rows[1].approvedById).toBe(ACTOR);
      expect(table.rows[1].approvedAt).toBeInstanceOf(Date);
      expect(audits).toEqual([
        {
          action: 'localai.catalog.approved',
          actorUserId: ACTOR,
          targetType: 'localai_catalog',
          targetId: '2026.11.03-1',
          meta: { source: 'editor', sourceRef: '2026.11.02-1', supersedes: '2026.11.02-1', notifyNodes: false, nodesNotified: 0 },
        },
      ]);
      expect(refresh.queueForEligibleNodes).not.toHaveBeenCalled();
    });

    it('approves with nothing previously approved', async () => {
      const { service, table } = build();
      table.rows.push(draftRow('2026.11.03-1', 'valid'));
      expect((await service.approve('2026.11.03-1', { notifyNodes: false }, ACTOR)).supersedes).toBeNull();
    });

    it('with notifyNodes queues the refresh once the approval is done, and a failure there does not undo it', async () => {
      const { service, table, refresh } = build();
      table.rows.push(draftRow('2026.11.03-1', 'valid'));
      const result = await service.approve('2026.11.03-1', { notifyNodes: true }, ACTOR);
      expect(refresh.queueForEligibleNodes).toHaveBeenCalledWith(ACTOR);
      expect(result.notified).toEqual({ queued: ['ai-n2'], alreadyQueued: [], skipped: 1 });
      expect(table.rows[0].status).toBe('approved');
    });

    it('answers 409 when two approvals race and the loser meets the unique index', async () => {
      const { service, table } = build();
      table.rows.push(draftRow('2026.11.03-1', 'valid'));
      const original = table.updateMany;
      table.updateMany = async (args: { where: Row; data: Row }) => {
        if (args.data.status === 'approved') throw Object.assign(new Error('unique'), { code: 'P2002' });
        return original(args);
      };
      expect(await status(service.approve('2026.11.03-1', { notifyNodes: false }, ACTOR))).toBe(409);
    });

    it('answers 404 for an unknown or malformed version', async () => {
      const { service } = build();
      expect(await status(service.approve('2099.01.01-1', { notifyNodes: false }, ACTOR))).toBe(404);
      expect(await status(service.approve('x', { notifyNodes: false }, ACTOR))).toBe(404);
    });
  });

  describe('reads', () => {
    it('lists versions newest first by version (numeric N), without file contents', async () => {
      const { service, table } = build();
      table.rows.push(draftRow('2026.11.02-9', 'superseded'), draftRow('2026.11.02-10', 'approved'), draftRow('2026.10.30-1', 'superseded'));
      const list = await service.list();
      expect(list.map((item) => item.version)).toEqual(['2026.11.02-10', '2026.11.02-9', '2026.10.30-1']);
      expect(list[0]).not.toHaveProperty('files');
    });

    it('returns the files, the lint result and, with diff=approved, the file-level change against the approved version', async () => {
      const { service, table } = build();
      table.rows.push(draftRow('2026.11.02-1', 'approved'));
      const next = draftRow('2026.11.03-1', 'valid', { lintResult: { valid: true, problems: [] } });
      next.files = toCatalogFiles([
        { path: 'CATALOG_VERSION', content: '2026.11.03-1\n' },
        { path: 'models.yaml', content: 'schema_version: 1\n# 2026.11.02-1\n' },
        { path: 'ark.yaml', content: 'new\n' },
      ]);
      table.rows.push(next);
      const detail = await service.get('2026.11.03-1', { diff: 'approved' });
      expect(detail.files.map((file) => file.path)).toEqual(['CATALOG_VERSION', 'models.yaml', 'ark.yaml']);
      expect(detail.lintResult).toEqual({ valid: true, problems: [] });
      expect(detail.diff).toEqual({
        against: '2026.11.02-1',
        files: [
          expect.objectContaining({ path: 'CATALOG_VERSION', change: 'changed' }),
          expect.objectContaining({ path: 'models.yaml', change: 'unchanged' }),
          expect.objectContaining({ path: 'ark.yaml', change: 'added' }),
        ],
      });
      expect((await service.get('2026.11.03-1')).diff).toBeUndefined();
    });
  });

  describe('forNode (the node route)', () => {
    it('answers missing when nothing is approved and no pin is set', async () => {
      const { service, table } = build();
      table.rows.push(draftRow('2026.11.03-1', 'valid'));
      expect(await service.forNode({ catalogPin: null }, undefined)).toEqual({ kind: 'missing', reason: 'no_approved_catalog' });
    });

    it('serves the approved catalog with a strong ETag, then 304 for that ETag only', async () => {
      const { service, table } = build();
      table.rows.push(draftRow('2026.11.02-1', 'superseded'), draftRow('2026.11.03-1', 'approved', { approvedAt: new Date('2026-11-03T10:00:00Z') }));
      const first = await service.forNode({ catalogPin: null }, undefined);
      expect(first).toMatchObject({ kind: 'ok', etag: `"${table.rows[1].bundleSha256}"`, body: { version: '2026.11.03-1', approvedAt: '2026-11-03T10:00:00.000Z' } });
      const again = await service.forNode({ catalogPin: null }, (first as { etag: string }).etag);
      expect(again).toEqual({ kind: 'not_modified', etag: (first as { etag: string }).etag });
      expect((await service.forNode({ catalogPin: null }, `"${table.rows[0].bundleSha256}"`)).kind).toBe('ok');
      expect((await service.forNode({ catalogPin: null }, `W/${(first as { etag: string }).etag}`)).kind).toBe('ok');
    });

    it('serves the pinned version whatever its status, with its own ETag, and reports a pin that does not exist', async () => {
      const { service, table } = build();
      table.rows.push(draftRow('2026.11.02-1', 'superseded'), draftRow('2026.11.03-1', 'approved'), draftRow('2026.11.04-1', 'draft'));
      const pinned = await service.forNode({ catalogPin: '2026.11.04-1' }, undefined);
      expect(pinned).toMatchObject({ kind: 'ok', body: { version: '2026.11.04-1', approvedAt: null } });
      // The approved catalog's ETag does not match the pinned node's catalog.
      expect((await service.forNode({ catalogPin: '2026.11.04-1' }, `"${table.rows[1].bundleSha256}"`)).kind).toBe('ok');
      expect(await service.forNode({ catalogPin: '2026.01.01-1' }, undefined)).toEqual({ kind: 'missing', reason: 'catalog_pin_not_found' });
    });
  });
});
