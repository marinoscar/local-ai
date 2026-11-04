// The catalog slice end to end against real SQL and the REAL `aictl` (docs/specs/cloud-app.md section 4.6, CLOUD-10):
// the whole app on a migrated database; the release archive is a local tarball built from the repository's own
// `catalog/` files (the import parses real files, so the test does too); the import and lint jobs are driven through
// their registered handlers (`JOBS_WORKER_MODE=off`, so no worker races the test); nodes are enrolled with real
// `nod_` credentials.
//
// What only real SQL proves here: the partial unique index (one approved row, however many approvals race), the
// approval transaction, the unique violation of a duplicate version, the command plane's active-dedup index, and the
// ETag/304 flow against the rows the node route really reads.
//
// The lint cases need `aictl` (`LOCALAI_AICTL_PATH`; cloud-ci builds it). Locally, without the variable, they are
// skipped with a message; in CI (`CI` set) a missing variable FAILS the suite instead of hiding the proof.
process.env.JOBS_WORKER_MODE = 'off';

import { createHash } from 'node:crypto';
import { chmodSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { NestFastifyApplication } from '@nestjs/platform-fastify';
import { JobHandlerRegistry, type Job } from '@marinoscar/platform-api/jobs';
import { NodeCredentialService } from '@marinoscar/platform-api/nodes';

import { CATALOG_ARCHIVE_FETCHER, type CatalogArchiveFetcher } from '../../src/localai/catalog/archive-fetcher';
import { createApp } from '../../src/main';
import { PrismaService } from '../../src/prisma/prisma.service';
import { buildCatalogArchive, repoCatalogFiles } from './helpers/tar-builder';

const HAS_AICTL = Boolean(process.env.LOCALAI_AICTL_PATH);
if (!HAS_AICTL && process.env.CI) throw new Error('LOCALAI_AICTL_PATH is not set in CI: the catalog lint proof must not be skipped (cloud-ci builds aictl).');
if (!HAS_AICTL) console.warn('catalog.db.spec: LOCALAI_AICTL_PATH is not set, so the real-aictl lint cases are SKIPPED (build aictl as cloud-ci does to run them).');
const itLint = HAS_AICTL ? it : it.skip;

const run = `${Date.now()}`;
const REPO_FILES = repoCatalogFiles();
const FIXTURE_VERSION = REPO_FILES.get('CATALOG_VERSION')!.toString('utf8').trim();
const FIXTURE_TAG = `catalog-${FIXTURE_VERSION}`;
const IMPORT = 'localai.catalog.import';
const LINT = 'localai.catalog.lint';

/** A digest computed here, independently of the app's code: sha256 of the bytes, and the bundle rule of section 3.6. */
const sha = (data: Buffer | string) => createHash('sha256').update(data).digest('hex');
function expectedBundle(files: Map<string, Buffer>): string {
  const lines = [...files].map(([path, bytes]) => `${path}\u0000${sha(bytes)}\n`).sort();
  return sha(lines.join(''));
}
/** The repository catalog as editor input, version rewritten the way the service does it. */
const editorFiles = (over: Record<string, string | null> = {}) =>
  [...REPO_FILES]
    .filter(([path]) => path !== 'CATALOG_VERSION')
    .flatMap(([path, bytes]) => (over[path] === null ? [] : [{ path, content: over[path] ?? bytes.toString('utf8') }]));

let app: NestFastifyApplication;
let prisma: PrismaService;
let registry: JobHandlerRegistry;
let credentials: NodeCredentialService;
let ownerId: string;
let adminToken: string;
let viewerToken: string;
let archive: Buffer;
const mintedCredentialIds: string[] = [];
const labNodeIds: string[] = [];
const tmpDirs: string[] = [];

const api = (method: 'GET' | 'POST', url: string, bearer: string, payload?: unknown, headers: Record<string, string> = {}) =>
  app.inject({ method, url: `/api/localai/catalog${url}`, headers: { authorization: `Bearer ${bearer}`, ...headers }, ...(payload === undefined ? {} : { payload: payload as object }) });
const admin = (method: 'GET' | 'POST', url: string, payload?: unknown) => api(method, url, adminToken, payload);

async function login(email: string, role: 'admin' | 'viewer'): Promise<{ token: string; id: string }> {
  const response = await app.inject({ method: 'POST', url: '/api/auth/test/login', payload: { email, role } });
  const token = new URL(String(response.headers.location)).searchParams.get('token') ?? '';
  return { token, id: (await prisma.user.findFirstOrThrow({ where: { email } })).id };
}

/** Runs the newest pending job of `type` for `version` through its registered handler, as the worker would. */
async function runJob(type: string, version: string): Promise<Job> {
  const job = await prisma.job.findFirstOrThrow({ where: { type, subjectId: version }, orderBy: { createdAt: 'desc' } });
  await registry.get(type)!.process(job as unknown as Job);
  return job as unknown as Job;
}

const versionRow = (version: string) => prisma.localaiCatalogVersion.findUniqueOrThrow({ where: { version } });
const auditRows = (action: string, targetId: string) => prisma.auditEvent.findMany({ where: { action, targetId } });
const approvedRows = () => prisma.localaiCatalogVersion.findMany({ where: { status: 'approved' } });

/** A catalog row inserted directly (no lint), for the tests that are about approval and serving, not about lint. */
async function insertVersion(version: string, status: 'draft' | 'valid' | 'invalid', marker: string) {
  const files = editorFiles({ 'models.yaml': `${REPO_FILES.get('models.yaml')!.toString('utf8')}\n# ${marker}\n` });
  const rows = [{ path: 'CATALOG_VERSION', content: `${version}\n` }, ...files].map(({ path, content }) => ({ path, content, sha256: sha(content) }));
  const bundle = sha(rows.map((row) => `${row.path}\u0000${row.sha256}\n`).sort().join(''));
  return prisma.localaiCatalogVersion.create({ data: { version, source: 'editor', status, files: rows, bundleSha256: bundle, createdById: ownerId } });
}

beforeAll(async () => {
  archive = buildCatalogArchive();
  app = await createApp();
  await app.init();
  await app.getHttpAdapter().getInstance().ready();
  prisma = app.get(PrismaService);
  registry = app.get(JobHandlerRegistry);
  credentials = app.get(NodeCredentialService);
  // This suite owns the table: a throwaway test database, and `--runInBand`.
  await prisma.localaiCatalogVersion.deleteMany({});
  const adminLogin = await login(`catalog-admin-${run}@example.test`, 'admin');
  adminToken = adminLogin.token;
  ownerId = adminLogin.id;
  viewerToken = (await login(`catalog-viewer-${run}@example.test`, 'viewer')).token;
  // The download goes to the local tarball, never to GitHub.
  const fetcher = app.get<CatalogArchiveFetcher>(CATALOG_ARCHIVE_FETCHER);
  jest.spyOn(fetcher, 'fetch').mockImplementation(async () => archive);
});

afterAll(async () => {
  jest.restoreAllMocks();
  if (prisma) {
    await prisma.localaiCommand.deleteMany({ where: { node: { nodeId: { in: labNodeIds } } } });
    await prisma.auditEvent.deleteMany({ where: { OR: [{ action: { startsWith: 'localai.catalog.' } }, { targetId: { in: labNodeIds } }] } });
    await prisma.localaiNode.deleteMany({ where: { nodeId: { in: labNodeIds } } });
    await prisma.localaiCatalogVersion.deleteMany({});
    await prisma.job.deleteMany({ where: { type: { in: [IMPORT, LINT] } } });
    await prisma.workerNode.deleteMany({ where: { createdById: ownerId } });
    await prisma.nodeCredential.deleteMany({ where: { id: { in: mintedCredentialIds } } });
  }
  for (const dir of tmpDirs) rmSync(dir, { recursive: true, force: true });
  await app?.close();
});

describe('the two catalog jobs', () => {
  it('are registered, single-attempt and server-only (never offered to a worker node)', () => {
    for (const type of [IMPORT, LINT]) {
      const handler = registry.get(type)!;
      expect(handler).toBeDefined();
      expect(handler.profile?.maxAttempts).toBe(1);
      expect(handler.nodeResultSchema).toBeUndefined();
      expect(handler.persistNodeResult).toBeUndefined();
      expect(registry.serverOnlyTypes()).toContain(type);
    }
  });
});

describe('import', () => {
  it('queues a job and answers 202; the job creates a draft with the digests of the archive bytes, then queues the lint', async () => {
    const response = await admin('POST', '/import', { tag: FIXTURE_TAG });
    expect(response.statusCode).toBe(202);
    expect((response.json() as { data: unknown }).data).toMatchObject({ tag: FIXTURE_TAG, version: FIXTURE_VERSION, jobId: expect.any(String) });
    // Queued, not done: no row yet, and the job carries no organization (the catalog is deployment-wide).
    expect(await prisma.localaiCatalogVersion.count()).toBe(0);
    const queued = await prisma.job.findFirstOrThrow({ where: { type: IMPORT, subjectId: FIXTURE_VERSION } });
    expect(queued).toMatchObject({ status: 'pending', subjectType: 'localai_catalog', orgId: null });
    // A second request while one is queued collapses onto it (active dedup), it never queues two downloads.
    expect((await admin('POST', '/import', { tag: FIXTURE_TAG })).statusCode).toBe(202);
    expect(await prisma.job.count({ where: { type: IMPORT, subjectId: FIXTURE_VERSION, status: 'pending' } })).toBe(1);

    await runJob(IMPORT, FIXTURE_VERSION);

    const row = await versionRow(FIXTURE_VERSION);
    expect(row).toMatchObject({ source: 'github_release', sourceRef: FIXTURE_TAG, status: 'draft', createdById: ownerId, lintedAt: null });
    expect(row.bundleSha256).toBe(expectedBundle(REPO_FILES));
    const files = row.files as Array<{ path: string; sha256: string; content: string }>;
    expect(files.map((file) => file.path)).toEqual(['CATALOG_VERSION', 'models.yaml', 'ark.yaml', ...[...REPO_FILES.keys()].filter((p) => p.startsWith('profiles/')).sort()]);
    for (const file of files) {
      expect(file.sha256).toBe(sha(REPO_FILES.get(file.path)!));
      expect(file.content).toBe(REPO_FILES.get(file.path)!.toString('utf8'));
    }
    expect(await prisma.job.count({ where: { type: LINT, subjectId: FIXTURE_VERSION } })).toBe(1);
    const imported = await auditRows('localai.catalog.imported', FIXTURE_VERSION);
    expect(imported).toHaveLength(1);
    expect(imported[0]).toMatchObject({ actorUserId: ownerId, targetType: 'localai_catalog', meta: { source: 'github_release', sourceRef: FIXTURE_TAG } });
  });

  it('answers 409 for a version that now exists, and the job refuses to overwrite it', async () => {
    const before = await versionRow(FIXTURE_VERSION);
    const response = await admin('POST', '/import', { tag: FIXTURE_TAG });
    expect(response.statusCode).toBe(409);
    expect((response.json() as { details: unknown }).details).toMatchObject({ reason: 'version_exists' });
    expect((await versionRow(FIXTURE_VERSION)).bundleSha256).toBe(before.bundleSha256);
  });

  it.each([
    ['a symlink under catalog/', { extra: [{ path: 'local-ai-catalog-fixture/catalog/profiles/evil.yaml', type: 'symlink' as const, linkTarget: '/etc/passwd' }] }],
    ['a hardlink under catalog/', { extra: [{ path: 'local-ai-catalog-fixture/catalog/ark2.yaml', type: 'hardlink' as const, linkTarget: 'x' }] }],
    ['an absolute path', { extra: [{ path: '/tmp/escape.yaml', content: 'x' }] }],
    ['a path traversal', { extra: [{ path: 'local-ai-catalog-fixture/catalog/../../escape.yaml', content: 'x' }] }],
  ])('the job refuses an archive with %s and stores nothing', async (_name, options) => {
    const version = '2099.03.01-1';
    jest.spyOn(app.get<CatalogArchiveFetcher>(CATALOG_ARCHIVE_FETCHER), 'fetch').mockResolvedValueOnce(buildCatalogArchive({ ...options, override: { CATALOG_VERSION: `${version}\n` } }));
    expect((await admin('POST', '/import', { tag: `catalog-${version}` })).statusCode).toBe(202);
    await expect(runJob(IMPORT, version)).rejects.toThrow(/Refused catalog-2099\.03\.01-1/);
    expect(await prisma.localaiCatalogVersion.count({ where: { version } })).toBe(0);
    await prisma.job.deleteMany({ where: { type: IMPORT, subjectId: version } });
  });

  it('a tag whose archive carries another CATALOG_VERSION is refused', async () => {
    const tag = 'catalog-2099.03.02-1';
    expect((await admin('POST', '/import', { tag })).statusCode).toBe(202);
    await expect(runJob(IMPORT, '2099.03.02-1')).rejects.toThrow(/version_mismatch/);
    expect(await prisma.localaiCatalogVersion.count({ where: { version: '2099.03.02-1' } })).toBe(0);
  });

  it('is for administrators only', async () => {
    expect((await api('POST', '/import', viewerToken, { tag: 'catalog-2099.03.03-1' })).statusCode).toBe(403);
    expect(await prisma.job.count({ where: { type: IMPORT, subjectId: '2099.03.03-1' } })).toBe(0);
  });
});

describe('lint with the real aictl', () => {
  itLint('makes the imported repository catalog valid and audits it', async () => {
    const result = await runJob(LINT, FIXTURE_VERSION);
    expect(result.type).toBe(LINT);
    const row = await versionRow(FIXTURE_VERSION);
    expect(row.status).toBe('valid');
    expect(row.lintResult).toMatchObject({ valid: true, problems: [], exitCode: 0 });
    expect(row.lintedAt).toBeInstanceOf(Date);
    const linted = await auditRows('localai.catalog.linted', FIXTURE_VERSION);
    expect(linted).toHaveLength(1);
    expect(linted[0].meta).toMatchObject({ source: 'github_release', sourceRef: FIXTURE_TAG, valid: true });
  });

  itLint('stores the problem list of an editor draft that is invalid (exit 2) and leaves it unapprovable', async () => {
    const version = '2099.01.01-1';
    const created = await admin('POST', '/versions', { version, baseVersion: FIXTURE_VERSION, notes: 'broken on purpose', files: editorFiles({ 'models.yaml': 'schema_version: 1\nunknown_key: 1\n' }) });
    expect(created.statusCode).toBe(201);
    expect((created.json() as { data: unknown }).data).toMatchObject({ version, source: 'editor', sourceRef: FIXTURE_VERSION, status: 'draft', notes: 'broken on purpose' });
    await runJob(LINT, version);
    const row = await versionRow(version);
    expect(row.status).toBe('invalid');
    const lint = row.lintResult as { valid: boolean; problems: Array<{ code: string; file: string; path: string; message: string }>; exitCode: number };
    expect(lint).toMatchObject({ valid: false, exitCode: 2 });
    expect(lint.problems.length).toBeGreaterThan(0);
    expect(lint.problems[0]).toEqual({ code: expect.stringMatching(/^LAI-CAT-\d{3}$/), file: expect.any(String), path: expect.any(String), message: expect.any(String) });

    const approve = await admin('POST', `/versions/${version}/approve`, {});
    expect(approve.statusCode).toBe(409);
    expect((approve.json() as { details: unknown }).details).toMatchObject({ reason: 'not_valid', status: 'invalid' });
    expect(await approvedRows()).toHaveLength(0);
  });

  itLint('an editor draft that is the repository catalog is valid; CATALOG_VERSION was rewritten to the new version', async () => {
    const version = '2099.01.02-1';
    const created = await admin('POST', '/versions', { version, files: [{ path: 'CATALOG_VERSION', content: 'ignored\n' }, ...editorFiles()] });
    expect(created.statusCode).toBe(201);
    await runJob(LINT, version);
    const row = await versionRow(version);
    expect(row.status).toBe('valid');
    const stored = (row.files as Array<{ path: string; content: string }>).find((file) => file.path === 'CATALOG_VERSION');
    expect(stored?.content).toBe(`${version}\n`);
  });

  itLint('re-lint is queued by the route and refused for history', async () => {
    const response = await admin('POST', `/versions/${FIXTURE_VERSION}/lint`, {});
    expect(response.statusCode).toBe(202);
    expect((response.json() as { data: unknown }).data).toMatchObject({ version: FIXTURE_VERSION, jobId: expect.any(String) });
    await runJob(LINT, FIXTURE_VERSION);
    expect((await versionRow(FIXTURE_VERSION)).status).toBe('valid');
    expect((await api('POST', `/versions/${FIXTURE_VERSION}/lint`, viewerToken, {})).statusCode).toBe(403);
  });

  it('any exit code other than 0 and 2 keeps the draft with lintResult.error and LAI-CLD-033 (fake binary)', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'catalog-db-fake-'));
    tmpDirs.push(dir);
    const fake = join(dir, 'aictl');
    writeFileSync(fake, '#!/bin/sh\necho "internal error" >&2\nexit 3\n');
    chmodSync(fake, 0o755);
    const saved = process.env.LOCALAI_AICTL_PATH;
    process.env.LOCALAI_AICTL_PATH = fake;
    try {
      const version = '2099.01.03-1';
      expect((await admin('POST', '/versions', { version, files: editorFiles() })).statusCode).toBe(201);
      await runJob(LINT, version);
      const row = await versionRow(version);
      expect(row.status).toBe('draft');
      expect(row.lintResult).toMatchObject({ error: { code: 'LAI-CLD-033', reason: 'aictl_failed' }, exitCode: 3 });
      const detail = (await admin('GET', `/versions/${version}`)).json() as { data: { status: string; lintError: { code: string } | null; lintResult: { error: unknown } } };
      expect(detail.data.status).toBe('draft');
      // A draft whose lint could not run is not approvable.
      expect((await admin('POST', `/versions/${version}/approve`, {})).statusCode).toBe(409);
    } finally {
      if (saved === undefined) delete process.env.LOCALAI_AICTL_PATH;
      else process.env.LOCALAI_AICTL_PATH = saved;
    }
  });
});

describe('editor and upload drafts', () => {
  it('answers 400 for a version that is not greater than every existing one (numeric N), whatever its status', async () => {
    await insertVersion('2099.04.01-9', 'draft', 'a');
    for (const version of ['2099.04.01-9', '2099.04.01-2', '2099.03.31-50']) {
      const response = await admin('POST', '/versions', { version, files: editorFiles() });
      expect(response.statusCode).toBe(400);
      expect((response.json() as { details: unknown }).details).toMatchObject({ reason: 'version_not_greater' });
    }
    expect((await admin('POST', '/versions', { version: '2099.04.01-10', files: editorFiles() })).statusCode).toBe(201);
  });

  it('answers 400 for a disallowed path and for a duplicate path', async () => {
    for (const path of ['README.md', '../models.yaml', 'profiles/Bad_Name.yaml', 'profiles/sub/x.yaml', '/etc/passwd']) {
      const response = await admin('POST', '/versions', { version: '2099.05.01-1', files: [{ path, content: 'x' }] });
      expect(response.statusCode).toBe(400);
    }
    const duplicate = await admin('POST', '/versions', { version: '2099.05.01-1', files: [{ path: 'models.yaml', content: 'a' }, { path: 'models.yaml', content: 'b' }] });
    expect(duplicate.statusCode).toBe(400);
    expect(await prisma.localaiCatalogVersion.count({ where: { version: '2099.05.01-1' } })).toBe(0);
  });

  it('answers 413 above 2 MB (the route accepts the body; the catalog rule refuses it) and accepts exactly 2 MB', async () => {
    const version = '2099.05.02-1';
    const room = 2 * 1024 * 1024 - Buffer.byteLength(`${version}\n`);
    const tooBig = await admin('POST', '/versions', { version, files: [{ path: 'models.yaml', content: 'a'.repeat(room + 1) }] });
    expect(tooBig.statusCode).toBe(413);
    expect((tooBig.json() as { details: unknown }).details).toMatchObject({ reason: 'catalog_too_large' });
    const exact = await admin('POST', '/versions', { version, files: [{ path: 'models.yaml', content: 'a'.repeat(room) }] });
    expect(exact.statusCode).toBe(201);
  });

  it('answers 404 for a base version that does not exist', async () => {
    expect((await admin('POST', '/versions', { version: '2099.06.01-1', baseVersion: '2000.01.01-1', files: editorFiles() })).statusCode).toBe(404);
  });

  it('records an upload as source=upload and is for administrators only', async () => {
    const response = await admin('POST', '/versions', { version: '2099.06.02-1', source: 'upload', files: editorFiles() });
    expect((response.json() as { data: { source: string } }).data.source).toBe('upload');
    expect((await api('POST', '/versions', viewerToken, { version: '2099.06.03-1', files: editorFiles() })).statusCode).toBe(403);
  });
});

describe('approval', () => {
  it('only a valid version can be approved; approval supersedes the previous one atomically', async () => {
    const first = await insertVersion('2099.07.01-1', 'valid', 'first');
    const second = await insertVersion('2099.07.02-1', 'valid', 'second');
    await insertVersion('2099.07.03-1', 'draft', 'draft');

    expect((await admin('POST', '/versions/2099.07.03-1/approve', {})).statusCode).toBe(409);
    expect((await api('POST', `/versions/${first.version}/approve`, viewerToken, {})).statusCode).toBe(403);
    expect(await approvedRows()).toHaveLength(0);

    const one = await admin('POST', `/versions/${first.version}/approve`, {});
    expect(one.statusCode).toBe(200);
    expect((one.json() as { data: { supersedes: unknown; notified: unknown } }).data).toMatchObject({ supersedes: null, notified: null });
    expect((await approvedRows()).map((row) => row.version)).toEqual([first.version]);
    expect(await versionRow(first.version)).toMatchObject({ status: 'approved', approvedById: ownerId });
    expect((await versionRow(first.version)).approvedAt).toBeInstanceOf(Date);

    const two = await admin('POST', `/versions/${second.version}/approve`, {});
    expect(two.statusCode).toBe(200);
    expect((two.json() as { data: { supersedes: string } }).data.supersedes).toBe(first.version);
    expect((await approvedRows()).map((row) => row.version)).toEqual([second.version]);
    expect((await versionRow(first.version)).status).toBe('superseded');

    // An approved or superseded version cannot be approved again.
    expect((await admin('POST', `/versions/${second.version}/approve`, {})).statusCode).toBe(409);
    expect((await admin('POST', `/versions/${first.version}/approve`, {})).statusCode).toBe(409);

    const approvedAudit = await auditRows('localai.catalog.approved', second.version);
    expect(approvedAudit).toHaveLength(1);
    expect(approvedAudit[0]).toMatchObject({ actorUserId: ownerId, meta: { supersedes: first.version, notifyNodes: false, nodesNotified: 0 } });
  });

  it('keeps exactly one approved row when several valid versions are approved at the same moment', async () => {
    const versions = Array.from({ length: 6 }, (_, i) => `2099.08.01-${i + 1}`);
    for (const version of versions) await insertVersion(version, 'valid', version);
    const responses = await Promise.all(versions.map((version) => admin('POST', `/versions/${version}/approve`, {})));
    const statuses = responses.map((response) => response.statusCode);
    expect(statuses.every((status) => status === 200 || status === 409)).toBe(true);
    const winners = statuses.filter((status) => status === 200).length;
    expect(winners).toBeGreaterThanOrEqual(1);

    const rows = await prisma.localaiCatalogVersion.findMany({ where: { version: { in: versions } } });
    const count = (status: string) => rows.filter((row) => row.status === status).length;
    // Every winner is approved or was superseded by a later winner; every loser (409) is still valid.
    expect(count('approved') + count('superseded')).toBe(winners);
    expect(count('valid')).toBe(statuses.length - winners);
    expect(await approvedRows()).toHaveLength(1);
    for (const response of responses.filter((r) => r.statusCode === 409)) {
      expect((response.json() as { details: { reason: string } }).details.reason).toMatch(/concurrent_approval|concurrent_change|not_valid/);
    }
  });

  it('the database refuses a second approved row whatever the application does', async () => {
    const [approved] = await approvedRows();
    const other = await insertVersion('2099.09.01-1', 'valid', 'index');
    await expect(prisma.localaiCatalogVersion.update({ where: { id: other.id }, data: { status: 'approved' } })).rejects.toMatchObject({ code: 'P2002' });
    expect((await approvedRows()).map((row) => row.id)).toEqual([approved.id]);
  });
});

describe('reading versions', () => {
  it('lists newest first without file contents and returns one version with files and the diff to the approved one', async () => {
    const list = (await api('GET', '/versions', viewerToken)).json() as { data: Array<{ version: string }> };
    const order = list.data.map((item) => item.version);
    const key = (version: string) => version.split(/[.-]/).map(Number);
    for (let i = 1; i < order.length; i += 1) {
      const [a, b] = [key(order[i - 1]), key(order[i])];
      const firstDifference = a.findIndex((value, index) => value !== b[index]);
      expect(firstDifference === -1 || a[firstDifference] > b[firstDifference]).toBe(true);
    }
    expect(list.data[0]).not.toHaveProperty('files');
    expect(order.indexOf('2099.09.01-1')).toBeLessThan(order.indexOf(FIXTURE_VERSION));

    const [approved] = await approvedRows();
    const detail = (await api('GET', '/versions/2099.09.01-1?diff=approved', viewerToken)).json() as {
      data: { files: unknown[]; diff: { against: string; files: Array<{ path: string; change: string }> } };
    };
    expect(detail.data.files.length).toBeGreaterThan(0);
    expect(detail.data.diff.against).toBe(approved.version);
    expect(detail.data.diff.files.find((file) => file.path === 'CATALOG_VERSION')?.change).toBe('changed');
    expect(detail.data.diff.files.find((file) => file.path === 'models.yaml')?.change).toBe('changed');
    expect(detail.data.diff.files.find((file) => file.path === 'ark.yaml')?.change).toBe('unchanged');
    expect((await api('GET', '/versions/2099.99.99-1', viewerToken)).statusCode).toBe(404);
  });
});

// ---------------------------------------------------------------------------------------------------------------
// The node route and the node notification, with real node credentials
// ---------------------------------------------------------------------------------------------------------------
describe('serving nodes', () => {
  async function enrolledNode(n: number, state: { mode?: string; stateAgeS?: number | null } = {}) {
    const nodeId = `ai-n${n}-cat-${run}`;
    const minted = await credentials.createCredential(ownerId, { name: `${nodeId} credential` } as never);
    mintedCredentialIds.push(minted.id);
    labNodeIds.push(nodeId);
    await prisma.localaiNode.create({
      data: { nodeId, displayName: `Node ${n}`, publicHost: `node-${n}.example.test`, servePattern: 'none', phase: 'pending', nodeCredentialId: minted.id, createdById: ownerId },
    });
    const registered = await app.inject({
      method: 'POST',
      url: '/api/nodes/register',
      headers: { authorization: `Bearer ${minted.token}` },
      payload: { name: nodeId, hostname: 'host-alpha', platform: 'linux/amd64', cliVersion: 'aiagent/0.1.0', eligibleTypes: ['localai.status.refresh'], concurrency: 1, capabilities: { localai: { schema: 1, agent_version: '0.1.0' } } },
    });
    const workerId = (registered.json() as { data: { node: { id: string } } }).data.node.id;
    // The first node call enrolls it.
    expect((await app.inject({ method: 'GET', url: `/api/nodes/${workerId}/localai/config`, headers: { authorization: `Bearer ${minted.token}` } })).statusCode).toBe(200);
    await prisma.localaiNode.update({
      where: { nodeId },
      data: { mode: state.mode ?? 'online', lastStateAt: state.stateAgeS === null ? null : new Date(Date.now() - (state.stateAgeS ?? 5) * 1000) },
    });
    return { nodeId, workerId, token: minted.token };
  }
  const catalog = (node: { workerId: string; token: string }, headers: Record<string, string> = {}) =>
    app.inject({ method: 'GET', url: `/api/nodes/${node.workerId}/localai/catalog`, headers: { authorization: `Bearer ${node.token}`, ...headers } });

  it('404 LAI-CLD-013 while nothing is approved, then 200 with the bundle after approval, 304 for its ETag, and a new ETag after the next approval', async () => {
    const node = await enrolledNode(21);
    // Nothing approved right now: step the table back to the state before the approval tests.
    await prisma.localaiCatalogVersion.updateMany({ where: { status: 'approved' }, data: { status: 'superseded' } });
    const none = await catalog(node);
    expect(none.statusCode).toBe(404);
    expect((none.json() as { details: unknown }).details).toEqual({ code: 'LAI-CLD-013', reason: 'no_approved_catalog' });

    const a = await insertVersion('2099.10.01-1', 'valid', 'serve-a');
    expect((await admin('POST', `/versions/${a.version}/approve`, {})).statusCode).toBe(200);
    const ok = await catalog(node);
    expect(ok.statusCode).toBe(200);
    expect(ok.headers.etag).toBe(`"${a.bundleSha256}"`);
    const body = ok.json() as { data: { version: string; bundleSha256: string; approvedAt: string; files: Array<{ path: string; sha256: string; content: string }> } };
    expect(body.data).toMatchObject({ version: a.version, bundleSha256: a.bundleSha256 });
    expect(Date.parse(body.data.approvedAt)).not.toBeNaN();
    // The node can verify exactly what it was sent: every file's sha256 is the digest of its content, and the bundle digest follows.
    for (const file of body.data.files) expect(file.sha256).toBe(sha(file.content));
    expect(sha(body.data.files.map((file) => `${file.path}\u0000${file.sha256}\n`).sort().join(''))).toBe(body.data.bundleSha256);
    expect(body.data.files[0].path).toBe('CATALOG_VERSION');

    const notModified = await catalog(node, { 'if-none-match': `"${a.bundleSha256}"` });
    expect(notModified.statusCode).toBe(304);
    expect(notModified.body).toBe('');

    const b = await insertVersion('2099.10.02-1', 'valid', 'serve-b');
    expect((await admin('POST', `/versions/${b.version}/approve`, {})).statusCode).toBe(200);
    const next = await catalog(node, { 'if-none-match': `"${a.bundleSha256}"` });
    expect(next.statusCode).toBe(200);
    expect(next.headers.etag).toBe(`"${b.bundleSha256}"`);
    expect((await catalog(node, { 'if-none-match': `"${b.bundleSha256}"` })).statusCode).toBe(304);
  });

  it('honours the pin per node: a pinned node gets its version (any status) and its own ETag, others keep the approved one', async () => {
    const plain = await enrolledNode(22);
    const pinnedNode = await enrolledNode(23);
    const draft = await insertVersion('2099.10.03-1', 'draft', 'pinned-draft');
    await prisma.localaiNode.update({ where: { nodeId: pinnedNode.nodeId }, data: { catalogPin: draft.version } });
    const [approved] = await approvedRows();

    const forPlain = await catalog(plain);
    const forPinned = await catalog(pinnedNode);
    expect((forPlain.json() as { data: { version: string } }).data.version).toBe(approved.version);
    expect((forPinned.json() as { data: { version: string; approvedAt: unknown } }).data).toMatchObject({ version: draft.version, approvedAt: null });
    expect(forPinned.headers.etag).toBe(`"${draft.bundleSha256}"`);

    // 304 is per node: each node's own ETag matches, the other node's does not.
    expect((await catalog(plain, { 'if-none-match': String(forPlain.headers.etag) })).statusCode).toBe(304);
    expect((await catalog(pinnedNode, { 'if-none-match': String(forPinned.headers.etag) })).statusCode).toBe(304);
    expect((await catalog(pinnedNode, { 'if-none-match': String(forPlain.headers.etag) })).statusCode).toBe(200);
    expect((await catalog(plain, { 'if-none-match': String(forPinned.headers.etag) })).statusCode).toBe(200);

    // A pin to a version that does not exist is a 404, never the approved one.
    await prisma.localaiNode.update({ where: { nodeId: pinnedNode.nodeId }, data: { catalogPin: '2000.01.01-1' } });
    const missing = await catalog(pinnedNode);
    expect(missing.statusCode).toBe(404);
    expect((missing.json() as { details: unknown }).details).toEqual({ code: 'LAI-CLD-013', reason: 'catalog_pin_not_found' });
  });

  it('refuses another node\'s credential and a browser session on the catalog route', async () => {
    const one = await enrolledNode(24);
    const two = await enrolledNode(25);
    const crossed = await app.inject({ method: 'GET', url: `/api/nodes/${two.workerId}/localai/catalog`, headers: { authorization: `Bearer ${one.token}` } });
    expect(crossed.statusCode).toBe(403);
    expect((crossed.json() as { details: unknown }).details).toMatchObject({ code: 'LAI-CLD-004', reason: 'credential_bound_to_other_node' });
    const session = await app.inject({ method: 'GET', url: `/api/nodes/${one.workerId}/localai/catalog`, headers: { authorization: `Bearer ${adminToken}` } });
    expect(session.statusCode).toBe(403);
  });

  it('notifyNodes goes through the command plane: a claim held by the node wakes at once with the catalog.refresh job', async () => {
    const node = await enrolledNode(36, { stateAgeS: 5 });
    const claim = (waitMs: number) =>
      app.inject({
        method: 'POST',
        url: `/api/nodes/${node.workerId}/localai/commands/claim`,
        headers: { authorization: `Bearer ${node.token}` },
        payload: { types: ['catalog.refresh', 'status.refresh'], limit: 1, waitMs },
      });
    const held = claim(10_000);
    await new Promise((resolve) => setTimeout(resolve, 150));

    const version = await insertVersion('2099.11.05-1', 'valid', 'notify-wake');
    const start = Date.now();
    expect((await admin('POST', `/versions/${version.version}/approve`, { notifyNodes: true })).statusCode).toBe(200);
    const answer = await held;
    expect(Date.now() - start).toBeLessThan(1500);
    expect(answer.statusCode).toBe(200);
    const [job] = (answer.json() as { data: { jobs: Array<{ job: { type: string; attempts: number }; params: unknown; renewIntervalMs: number }> } }).data.jobs;
    expect(job).toMatchObject({ job: { type: 'localai.catalog.refresh', attempts: 1 }, params: {}, renewIntervalMs: 20_000 });
    // The command row is the one the policy table describes: 1 h TTL, source and requester recorded.
    const row = await prisma.localaiCommand.findFirstOrThrow({ where: { node: { nodeId: node.nodeId }, type: 'localai.catalog.refresh' } });
    expect(row).toMatchObject({ status: 'running', source: 'catalog_approve', requestedById: ownerId, dedupKey: 'catalog.refresh:-' });
    expect(Math.abs(row.expiresAt.getTime() - row.createdAt.getTime() - 60 * 60 * 1000)).toBeLessThan(2_000); // created_at is the database's clock
  });

  it('notifyNodes queues one catalog.refresh per healthy or degraded enrolled node, none for quiet or down nodes, and never stacks a second', async () => {
    const healthy = await enrolledNode(31, { stateAgeS: 5 });
    const degraded = await enrolledNode(32, { stateAgeS: 300 });
    const down = await enrolledNode(33, { stateAgeS: 3600 });
    const quiet = await enrolledNode(34, { mode: 'offline', stateAgeS: 5 });
    const silent = await enrolledNode(35, { stateAgeS: null });
    const mine = [healthy, degraded, down, quiet, silent];
    const commandsOf = (nodeId: string) => prisma.localaiCommand.findMany({ where: { node: { nodeId }, type: 'localai.catalog.refresh' } });

    const version = await insertVersion('2099.11.01-1', 'valid', 'notify-1');
    const response = await admin('POST', `/versions/${version.version}/approve`, { notifyNodes: true });
    expect(response.statusCode).toBe(200);
    const notified = (response.json() as { data: { notified: { queued: string[]; alreadyQueued: string[]; skipped: number } } }).data.notified;
    for (const node of [healthy, degraded]) expect(notified.queued).toContain(node.nodeId);
    for (const node of [down, quiet, silent]) expect(notified.queued).not.toContain(node.nodeId);

    for (const node of [healthy, degraded]) {
      const commands = await commandsOf(node.nodeId);
      expect(commands).toHaveLength(1);
      expect(commands[0]).toMatchObject({ status: 'queued', params: {}, dedupKey: 'catalog.refresh:-', source: 'catalog_approve', requestedById: ownerId, attempts: 0 });
      expect(commands[0].expiresAt.getTime()).toBeGreaterThan(Date.now() + 50 * 60 * 1000);
    }
    for (const node of [down, quiet, silent]) expect(await commandsOf(node.nodeId)).toHaveLength(0);
    expect(mine.length).toBe(5);

    // Approving again while the first commands are still queued adds nothing (the active-dedup index).
    const again = await insertVersion('2099.11.02-1', 'valid', 'notify-2');
    const second = await admin('POST', `/versions/${again.version}/approve`, { notifyNodes: true });
    expect(second.statusCode).toBe(200);
    const secondNotified = (second.json() as { data: { notified: { queued: string[]; alreadyQueued: string[] } } }).data.notified;
    expect(secondNotified.alreadyQueued).toEqual(expect.arrayContaining([healthy.nodeId, degraded.nodeId]));
    expect(secondNotified.queued).not.toContain(healthy.nodeId);
    for (const node of [healthy, degraded]) expect(await commandsOf(node.nodeId)).toHaveLength(1);

    const requested = await prisma.auditEvent.findMany({ where: { action: 'localai.command.requested', actorUserId: ownerId } });
    expect(requested.length).toBeGreaterThanOrEqual(2);
    expect(JSON.stringify(requested)).not.toContain(healthy.token);

    // Without notifyNodes nothing is queued.
    const quietApprove = await insertVersion('2099.11.03-1', 'valid', 'notify-3');
    const before = await prisma.localaiCommand.count({ where: { type: 'localai.catalog.refresh' } });
    expect((await admin('POST', `/versions/${quietApprove.version}/approve`, {})).statusCode).toBe(200);
    expect(await prisma.localaiCommand.count({ where: { type: 'localai.catalog.refresh' } })).toBe(before);
  });
});
