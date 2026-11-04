// The catalog versions: import, editor drafts, lint, approval and serving (docs/specs/cloud-app.md section 4.6).
//
// State machine of a version (`localai_catalog_versions.status`):
//
//   draft --lint ok--> valid --approve--> approved --a newer one is approved--> superseded
//     |  \--lint found problems--> invalid (re-lint after the files are fixed means a NEW version: files are immutable)
//     \--lint could not run (aictl missing, timeout...) stays draft, `lint_result.error` says why (LAI-CLD-033)
//
// Files never change after a row is created, so a verdict never goes stale. Approval is one transaction; the partial
// unique index `localai_catalog_one_approved` is the guarantee that at most one row is approved, whatever races.
//
// Long work is never done in a request: the download and the lint are jobs (`catalog-import.job.ts`,
// `catalog-lint.job.ts`) that call `importFromArchive` and `runLint` below.
import { BadRequestException, ConflictException, Inject, Injectable, Logger, NotFoundException, PayloadTooLargeException } from '@nestjs/common';
import { AUDIT_SINK, type AuditSink } from '@marinoscar/platform-api/core';
import { JobsService } from '@marinoscar/platform-api/jobs';
import type { LocalaiCatalogVersion, LocalaiNode, Prisma } from '@prisma/client';

import { PrismaService } from '../../prisma/prisma.service';
import { ArchiveFetchError, archiveUrlFor, CATALOG_ARCHIVE_FETCHER, type CatalogArchiveFetcher } from './archive-fetcher';
import { bundleSha256 } from './bundle-hash';
import { CatalogRefreshQueue, type RefreshQueueResult } from './catalog-refresh.queue';
import { CatalogSettings } from './catalog-settings';
import { diffCatalogs, type FileDiff } from './catalog-diff';
import { CatalogValidationError, editorPayloadBytes, filesFromArchive, filesFromEditor } from './catalog-files';
import { CATALOG_LINTER, type CatalogLinter } from './catalog-linter';
import {
  AUDIT_CATALOG_APPROVED,
  AUDIT_CATALOG_IMPORTED,
  AUDIT_CATALOG_LINTED,
  AUDIT_CATALOG_TARGET,
  CATALOG_JOB_SUBJECT,
  JOB_CATALOG_IMPORT,
  JOB_CATALOG_LINT,
  LAI_CLD_CATALOG_UNAVAILABLE,
  MAX_CATALOG_BYTES,
  type CatalogFile,
  type LintResult,
} from './catalog.constants';
import type { ApproveBody, CreateVersionBody } from './catalog.schemas';
import { CatalogArchiveError, extractCatalogFiles } from './tar-extract';
import { compareCatalogVersions, isCatalogVersion, isGreaterThanAll, versionOfCatalogTag } from './version';
import { etagOf, ifNoneMatchMatches } from './etag';

/** The statuses a lint may still change. `approved` and `superseded` are history. */
const LINTABLE = ['draft', 'invalid', 'valid'] as const;

export interface CatalogVersionSummary {
  id: string;
  version: string;
  source: LocalaiCatalogVersion['source'];
  sourceRef: string | null;
  status: LocalaiCatalogVersion['status'];
  bundleSha256: string;
  lintedAt: string | null;
  /** Number of problems the last lint found (0 when valid or not yet linted). */
  problemCount: number;
  /** The reason the last lint could not run, when it could not. */
  lintError: NonNullable<LintResult['error']> | null;
  notes: string | null;
  createdById: string | null;
  createdAt: string;
  approvedById: string | null;
  approvedAt: string | null;
}

export interface CatalogVersionDetail extends CatalogVersionSummary {
  files: CatalogFile[];
  lintResult: LintResult | null;
  /** Present with `?diff=approved`: this version against the approved one (null `against` when none is approved). */
  diff?: { against: string | null; files: FileDiff[] };
}

export interface NodeCatalogBody {
  version: string;
  bundleSha256: string;
  approvedAt: string | null;
  files: CatalogFile[];
}

export type NodeCatalogAnswer =
  | { kind: 'ok'; etag: string; body: NodeCatalogBody }
  | { kind: 'not_modified'; etag: string }
  | { kind: 'missing'; reason: 'no_approved_catalog' | 'catalog_pin_not_found' };

function isUniqueViolation(error: unknown): boolean {
  return typeof error === 'object' && error !== null && (error as { code?: string }).code === 'P2002';
}

function isCatalogFile(value: unknown): value is CatalogFile {
  if (typeof value !== 'object' || value === null) return false;
  const file = value as Record<string, unknown>;
  return typeof file.path === 'string' && typeof file.sha256 === 'string' && typeof file.content === 'string';
}

/** The stored `files` column as typed files; a malformed column (never written by this code) is an error, not a guess. */
function readFiles(json: unknown): CatalogFile[] {
  if (!Array.isArray(json) || !json.every(isCatalogFile)) throw new Error('localai_catalog_versions.files is malformed');
  return json;
}

function readLintResult(json: unknown): LintResult | null {
  return typeof json === 'object' && json !== null && !Array.isArray(json) ? (json as LintResult) : null;
}

function describe(error: unknown): string {
  return error instanceof Error ? error.name : 'unknown error';
}

@Injectable()
export class CatalogService {
  private readonly logger = new Logger(CatalogService.name);

  constructor(
    private readonly prisma: PrismaService,
    private readonly jobs: JobsService,
    private readonly settings: CatalogSettings,
    private readonly refreshQueue: CatalogRefreshQueue,
    @Inject(AUDIT_SINK) private readonly audit: AuditSink,
    @Inject(CATALOG_ARCHIVE_FETCHER) private readonly fetcher: CatalogArchiveFetcher,
    @Inject(CATALOG_LINTER) private readonly linter: CatalogLinter,
  ) {}

  // -------------------------------------------------------------------------------------------------------------
  // Reads
  // -------------------------------------------------------------------------------------------------------------

  /** Every version, newest first (by version, not by creation time), without file contents. */
  async list(): Promise<CatalogVersionSummary[]> {
    const rows = await this.prisma.localaiCatalogVersion.findMany({ omit: { files: true } });
    return rows
      .map((row) => this.summarize(row))
      .sort((a, b) => (isCatalogVersion(a.version) && isCatalogVersion(b.version) ? compareCatalogVersions(b.version, a.version) : 0));
  }

  async get(version: string, options: { diff?: 'approved' } = {}): Promise<CatalogVersionDetail> {
    const row = await this.requireRow(version);
    const files = readFiles(row.files);
    const detail: CatalogVersionDetail = { ...this.summarize(row), files, lintResult: readLintResult(row.lintResult) };
    if (options.diff === 'approved') {
      const approved = await this.prisma.localaiCatalogVersion.findFirst({ where: { status: 'approved' } });
      detail.diff = {
        against: approved?.version ?? null,
        files: diffCatalogs(approved ? readFiles(approved.files) : [], files),
      };
    }
    return detail;
  }

  // -------------------------------------------------------------------------------------------------------------
  // Creating versions
  // -------------------------------------------------------------------------------------------------------------

  /**
   * `POST /import`: checks the tag, refuses a version we already have (409) and queues the download. The download,
   * the extraction and the first lint run in the `localai.catalog.import` job.
   */
  async requestImport(tag: string, actorUserId: string): Promise<{ tag: string; version: string; jobId: string }> {
    const version = versionOfCatalogTag(tag);
    if (!version) throw new BadRequestException({ message: 'The tag must look like catalog-2026.11.02-1.', details: { reason: 'bad_tag' } });
    if (await this.prisma.localaiCatalogVersion.findUnique({ where: { version }, select: { id: true } })) {
      throw new ConflictException({ message: `Catalog ${version} already exists.`, details: { reason: 'version_exists', version } });
    }
    const job = await this.jobs.enqueue({
      type: JOB_CATALOG_IMPORT,
      reason: 'upload',
      subjectType: CATALOG_JOB_SUBJECT,
      subjectId: version,
      payload: { tag, actorUserId },
      orgId: null,
    });
    return { tag, version, jobId: job.id };
  }

  /** `POST /versions`: an editor or upload draft. Returns the draft; the lint is queued. */
  async createDraft(body: CreateVersionBody, actorUserId: string): Promise<CatalogVersionSummary> {
    // Cheap checks first, on the request as it came.
    if (editorPayloadBytes(body.files) > MAX_CATALOG_BYTES) throw this.tooLarge();
    const existing = await this.prisma.localaiCatalogVersion.findMany({ select: { version: true } });
    if (!isGreaterThanAll(body.version, existing.map((row) => row.version))) {
      throw new BadRequestException({
        message: `Version ${body.version} must be greater than every existing version.`,
        details: { reason: 'version_not_greater', version: body.version },
      });
    }
    if (body.baseVersion && !existing.some((row) => row.version === body.baseVersion)) {
      throw new NotFoundException({ message: `Base version ${body.baseVersion} was not found.`, details: { reason: 'base_version_not_found' } });
    }
    let files: CatalogFile[];
    try {
      files = filesFromEditor(body.files, body.version);
    } catch (error) {
      throw this.fromValidation(error);
    }
    const row = await this.insertDraft({
      version: body.version,
      source: body.source,
      sourceRef: body.baseVersion ?? null,
      files,
      notes: body.notes ?? null,
      actorUserId,
    });
    await this.queueLint(row.version, actorUserId, 'upload');
    return this.summarize(row);
  }

  /**
   * The body of the `localai.catalog.import` job: download the tag's archive, extract `catalog/**`, store the draft,
   * audit it and queue the lint. Throws on any failure (the job is single-attempt; the job list shows the message).
   */
  async importFromArchive(tag: string, actorUserId: string | null): Promise<CatalogVersionSummary> {
    const version = versionOfCatalogTag(tag);
    if (!version) throw new Error('The import job was queued with a tag that is not catalog-<version>.');

    const existing = await this.prisma.localaiCatalogVersion.findUnique({ where: { version } });
    if (existing) {
      // Never overwrite. The same tag already imported is a re-run after a crash between the insert and the lint
      // request (the queue is at-least-once): finish the job by queueing the lint that may be missing.
      if (existing.sourceRef !== tag || existing.source !== 'github_release') {
        throw new Error(`Catalog ${version} already exists, so ${tag} was not imported.`);
      }
      if (existing.status === 'draft' && existing.lintedAt === null) await this.queueLint(version, actorUserId, 'upload');
      return this.summarize(existing);
    }

    const repo = await this.settings.githubRepo();
    const url = archiveUrlFor(repo, tag);
    if (!url) throw new Error('The catalog.githubRepo setting is not a GitHub <owner>/<repo>.');

    let files: CatalogFile[];
    try {
      const archive = await this.fetcher.fetch(url);
      const extracted = await extractCatalogFiles(archive);
      files = filesFromArchive(extracted, version).files;
    } catch (error) {
      if (error instanceof ArchiveFetchError) throw new Error(`${LAI_CLD_CATALOG_UNAVAILABLE}: could not download ${tag} (${error.reason}).`);
      if (error instanceof CatalogArchiveError) throw new Error(`Refused ${tag}: ${error.message} (${error.code}).`);
      if (error instanceof CatalogValidationError) throw new Error(`Refused ${tag}: ${error.message} (${error.kind}).`);
      throw error;
    }

    const row = await this.insertDraft({ version, source: 'github_release', sourceRef: tag, files, notes: null, actorUserId });
    await this.queueLint(version, actorUserId, 'upload');
    return this.summarize(row);
  }

  private async insertDraft(input: {
    version: string;
    source: LocalaiCatalogVersion['source'];
    sourceRef: string | null;
    files: CatalogFile[];
    notes: string | null;
    actorUserId: string | null;
  }): Promise<LocalaiCatalogVersion> {
    let row: LocalaiCatalogVersion;
    try {
      row = await this.prisma.localaiCatalogVersion.create({
        data: {
          version: input.version,
          source: input.source,
          sourceRef: input.sourceRef,
          status: 'draft',
          files: input.files as unknown as Prisma.InputJsonValue,
          bundleSha256: bundleSha256(input.files),
          notes: input.notes,
          createdById: input.actorUserId,
        },
      });
    } catch (error) {
      if (isUniqueViolation(error)) {
        throw new ConflictException({ message: `Catalog ${input.version} already exists.`, details: { reason: 'version_exists', version: input.version } });
      }
      throw error;
    }
    await this.auditEvent(AUDIT_CATALOG_IMPORTED, input.actorUserId, input.version, { source: input.source, sourceRef: input.sourceRef });
    return row;
  }

  // -------------------------------------------------------------------------------------------------------------
  // Lint
  // -------------------------------------------------------------------------------------------------------------

  /** `POST /versions/:version/lint`: queues a (re-)lint. History (`approved`, `superseded`) is not re-linted. */
  async requestLint(version: string, actorUserId: string): Promise<{ version: string; jobId: string }> {
    const row = await this.requireRow(version);
    if (!(LINTABLE as readonly string[]).includes(row.status)) {
      throw new ConflictException({ message: `Catalog ${version} is ${row.status} and cannot be linted again.`, details: { reason: 'not_lintable', status: row.status } });
    }
    return { version, jobId: await this.queueLint(version, actorUserId, 'rerun') };
  }

  private async queueLint(version: string, actorUserId: string | null, reason: 'upload' | 'rerun'): Promise<string> {
    const job = await this.jobs.enqueue({
      type: JOB_CATALOG_LINT,
      reason,
      subjectType: CATALOG_JOB_SUBJECT,
      subjectId: version,
      payload: { version, actorUserId },
      orgId: null,
    });
    return job.id;
  }

  /**
   * The body of the `localai.catalog.lint` job: run the real linter over the stored files and record the verdict.
   * The status is only written while the row is still lintable, so a lint that finishes after an approval never
   * overwrites it. Always returns normally once a verdict (or a recorded failure) exists.
   */
  async runLint(version: string, actorUserId: string | null): Promise<void> {
    const row = await this.prisma.localaiCatalogVersion.findUnique({ where: { version } });
    if (!row) {
      this.logger.warn(`lint requested for catalog_version=${version}, which no longer exists`);
      return;
    }
    if (!(LINTABLE as readonly string[]).includes(row.status)) return;

    const outcome = await this.linter.lint(readFiles(row.files));
    const written = await this.prisma.localaiCatalogVersion.updateMany({
      where: { id: row.id, status: { in: [...LINTABLE] } },
      data: { status: outcome.status, lintResult: outcome.result as unknown as Prisma.InputJsonValue, lintedAt: new Date() },
    });
    if (outcome.result.error) {
      this.logger.error(`catalog lint could not run for catalog_version=${version}: ${outcome.result.error.code} ${outcome.result.error.reason}`);
    }
    if (written.count === 1) {
      await this.auditEvent(AUDIT_CATALOG_LINTED, actorUserId, version, { source: row.source, sourceRef: row.sourceRef, valid: outcome.status === 'valid' });
    }
  }

  // -------------------------------------------------------------------------------------------------------------
  // Approval
  // -------------------------------------------------------------------------------------------------------------

  /**
   * `POST /versions/:version/approve`: `valid` -> `approved`, the previous approved -> `superseded`, one transaction.
   * With `notifyNodes`, a `catalog.refresh` command is queued on each eligible node AFTER the commit.
   */
  async approve(
    version: string,
    body: ApproveBody,
    actorUserId: string,
  ): Promise<{ version: CatalogVersionSummary; supersedes: string | null; notified: RefreshQueueResult | null }> {
    const row = await this.requireRow(version);
    if (row.status !== 'valid') {
      throw new ConflictException({
        message: `Catalog ${version} is ${row.status}; only a valid catalog can be approved.`,
        details: { reason: 'not_valid', status: row.status },
      });
    }

    let supersedes: string | null = null;
    let approved: LocalaiCatalogVersion;
    try {
      approved = await this.prisma.$transaction(async (tx) => {
        const previous = await tx.localaiCatalogVersion.findFirst({ where: { status: 'approved' }, select: { version: true } });
        // Supersede first: the unique index admits one approved row, so the old one must leave `approved` before
        // the new one enters it.
        await tx.localaiCatalogVersion.updateMany({ where: { status: 'approved' }, data: { status: 'superseded' } });
        const result = await tx.localaiCatalogVersion.updateMany({
          where: { id: row.id, status: 'valid' },
          data: { status: 'approved', approvedById: actorUserId, approvedAt: new Date() },
        });
        if (result.count !== 1) throw new ConflictException({ message: `Catalog ${version} changed while it was being approved.`, details: { reason: 'concurrent_change' } });
        supersedes = previous?.version ?? null;
        return tx.localaiCatalogVersion.findUniqueOrThrow({ where: { id: row.id } });
      });
    } catch (error) {
      // Two approvals racing: the loser meets the index. Its transaction rolled back, so nothing changed.
      if (isUniqueViolation(error)) {
        throw new ConflictException({ message: 'Another catalog was approved at the same moment. Reload and try again.', details: { reason: 'concurrent_approval' } });
      }
      throw error;
    }

    // After the commit and outside the transaction: the audit row and the node commands.
    const notified = body.notifyNodes ? await this.refreshQueue.queueForEligibleNodes(actorUserId) : null;
    await this.auditEvent(AUDIT_CATALOG_APPROVED, actorUserId, version, {
      source: approved.source,
      sourceRef: approved.sourceRef,
      supersedes,
      notifyNodes: body.notifyNodes,
      nodesNotified: notified ? notified.queued.length : 0,
    });
    return { version: this.summarize(approved), supersedes, notified };
  }

  // -------------------------------------------------------------------------------------------------------------
  // Serving a node
  // -------------------------------------------------------------------------------------------------------------

  /**
   * What `GET /api/nodes/:id/localai/catalog` answers for `node`: its `catalogPin` version if set (whatever its
   * status), else the approved one. The strong ETag is the `bundleSha256`.
   */
  async forNode(node: Pick<LocalaiNode, 'catalogPin'>, ifNoneMatch: string | string[] | undefined): Promise<NodeCatalogAnswer> {
    const row = node.catalogPin
      ? await this.prisma.localaiCatalogVersion.findUnique({ where: { version: node.catalogPin } })
      : await this.prisma.localaiCatalogVersion.findFirst({ where: { status: 'approved' } });
    if (!row) return { kind: 'missing', reason: node.catalogPin ? 'catalog_pin_not_found' : 'no_approved_catalog' };
    const etag = etagOf(row.bundleSha256);
    if (ifNoneMatchMatches(ifNoneMatch, row.bundleSha256)) return { kind: 'not_modified', etag };
    return {
      kind: 'ok',
      etag,
      body: { version: row.version, bundleSha256: row.bundleSha256, approvedAt: row.approvedAt?.toISOString() ?? null, files: readFiles(row.files) },
    };
  }

  // -------------------------------------------------------------------------------------------------------------
  // Helpers
  // -------------------------------------------------------------------------------------------------------------

  private async requireRow(version: string): Promise<LocalaiCatalogVersion> {
    const row = isCatalogVersion(version) ? await this.prisma.localaiCatalogVersion.findUnique({ where: { version } }) : null;
    if (!row) throw new NotFoundException({ message: `Catalog ${version.slice(0, 40)} was not found.`, details: { reason: 'version_not_found' } });
    return row;
  }

  private summarize(row: Omit<LocalaiCatalogVersion, 'files'> & { files?: unknown }): CatalogVersionSummary {
    const lint = readLintResult(row.lintResult);
    return {
      id: row.id,
      version: row.version,
      source: row.source,
      sourceRef: row.sourceRef,
      status: row.status,
      bundleSha256: row.bundleSha256,
      lintedAt: row.lintedAt?.toISOString() ?? null,
      problemCount: lint?.problems?.length ?? 0,
      lintError: lint?.error ?? null,
      notes: row.notes,
      createdById: row.createdById,
      createdAt: row.createdAt.toISOString(),
      approvedById: row.approvedById,
      approvedAt: row.approvedAt?.toISOString() ?? null,
    };
  }

  private tooLarge(): PayloadTooLargeException {
    return new PayloadTooLargeException({ message: 'The catalog is larger than 2 MB.', details: { reason: 'catalog_too_large' } });
  }

  private fromValidation(error: unknown): Error {
    if (error instanceof CatalogValidationError) {
      if (error.kind === 'too_large') return this.tooLarge();
      return new BadRequestException({ message: error.message, details: { reason: error.kind } });
    }
    return error instanceof Error ? error : new Error('unknown error');
  }

  /** Audit runs after the write it records; a failure is logged (the error name only) and never undoes the write. */
  private async auditEvent(action: string, actorUserId: string | null, version: string, meta: Record<string, string | number | boolean | null>): Promise<void> {
    try {
      await this.audit.record({ action, actorUserId, targetType: AUDIT_CATALOG_TARGET, targetId: version, meta });
    } catch (error) {
      this.logger.error(`audit ${action} failed for catalog_version=${version}: ${describe(error)}`);
    }
  }
}
