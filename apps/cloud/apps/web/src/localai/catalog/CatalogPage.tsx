// The Catalog page (`/lab/catalog`, lab:read; actions need lab:admin; docs/specs/cloud-app.md 3.11, 4.6): the approved
// version and how many nodes are on it, the versions table, and under it one panel at a time (the draft editor or a
// diff). Enrolled nodes download only the approved catalog; this page imports a release, edits a draft, shows the
// lint problems of the real `aictl catalog lint`, diffs against the approved version and approves.
import AddIcon from '@mui/icons-material/Add';
import DownloadIcon from '@mui/icons-material/Download';
import { Alert, Box, Button, CircularProgress, Typography } from '@mui/material';
import { usePermissions } from '@marinoscar/platform-web/identity/headless';
import { useCallback, useRef, useState } from 'react';
import { Link as RouterLink } from 'react-router-dom';

import { catalogApi, type ApproveResponse, type CatalogApi, type CatalogVersionSummary } from '../api/catalog';
import { useLabNodes, type FetchLabNodes } from '../lab/useLabNodes';
import { ApproveDialog } from './ApproveDialog';
import { CatalogEditor, type EditorSource } from './CatalogEditor';
import { DiffPanel } from './DiffView';
import { ImportDialog } from './ImportDialog';
import { useCatalogDiff, useCatalogVersions } from './useCatalog';
import { VersionTable } from './VersionTable';

type Panel = { kind: 'editor'; source: EditorSource } | { kind: 'diff'; version: string } | null;

/** "Approved: 2026.11.02-1 (catalog-2026.11.02-1) · approved by Person A · 2 of 3 nodes on it" */
export function approvedHeadline(approved: CatalogVersionSummary | undefined, nodesOnIt: { on: number; total: number } | null): string {
  if (!approved) return 'Nothing is approved yet. Enrolled nodes keep the catalog they already have.';
  const parts = [`Approved: ${approved.version}${approved.sourceRef ? ` (${approved.sourceRef})` : ''}`, `approved by ${approved.approvedByName ?? 'an administrator'}`];
  if (nodesOnIt) parts.push(`${nodesOnIt.on} of ${nodesOnIt.total} ${nodesOnIt.total === 1 ? 'node' : 'nodes'} on it`);
  return parts.join(' · ');
}

export interface CatalogPageProps {
  /** Replaces the transport (tests). */
  client?: CatalogApi;
  /** Replaces the Lab overview transport, which gives the node counts and the nodes to push to (tests). */
  fetchNodes?: FetchLabNodes;
  /** The instant the version suggestion counts from (tests). */
  today?: Date;
}

export function CatalogPage({ client = catalogApi, fetchNodes, today }: CatalogPageProps) {
  const { hasPermission } = usePermissions();
  // Writes (import, edit, lint, approve, push) are for lab:admin, the permission the API enforces on them.
  const canWrite = hasPermission('lab:admin');

  // Versions whose re-lint was asked for -> the `lintedAt` they had then; the row reads "checking…" until it changes.
  const [pending, setPending] = useState<Record<string, string | null>>({});
  const list = useCatalogVersions(client, pending);
  const nodes = useLabNodes(fetchNodes);

  const [panel, setPanel] = useState<Panel>(null);
  const [approving, setApproving] = useState<string | null>(null);
  const [importOpen, setImportOpen] = useState(false);
  const [notice, setNotice] = useState<{ severity: 'success' | 'error' | 'info'; text: string } | null>(null);
  const [pushing, setPushing] = useState(false);
  const dirty = useRef(false);
  const onDirtyChange = useCallback((value: boolean) => {
    dirty.current = value;
  }, []);

  const versions = list.data ?? [];
  const approved = versions.find((version) => version.status === 'approved');
  const liveNodes = (nodes.data?.nodes ?? []).filter((node) => node.phase !== 'retired');
  const nodesOnIt = nodes.data && approved ? { on: liveNodes.filter((node) => node.catalogVersion === approved.version).length, total: liveNodes.length } : null;

  /** Replacing the open panel throws away unsaved edits: ask first. */
  const canLeavePanel = () => !dirty.current || window.confirm('Discard your unsaved changes?');
  const openPanel = (next: Panel) => {
    if (!canLeavePanel()) return;
    dirty.current = false;
    setPanel(next);
  };

  const startDraft = () => openPanel({ kind: 'editor', source: { kind: 'new', baseVersion: approved?.version ?? versions[0]?.version ?? null } });

  const lintAgain = async (version: string) => {
    const before = versions.find((item) => item.version === version)?.lintedAt ?? null;
    setPending((current) => ({ ...current, [version]: before }));
    try {
      await client.lint(version);
      list.refresh();
    } catch (caught) {
      setPending((current) => {
        const next = { ...current };
        delete next[version];
        return next;
      });
      setNotice({ severity: 'error', text: caught instanceof Error && caught.message !== 'Request failed' ? caught.message : `Could not queue the lint for ${version}.` });
    }
  };

  const pushToNodes = async () => {
    const targets = liveNodes.filter((node) => node.phase === 'enrolled' && (node.health === 'healthy' || node.health === 'degraded'));
    if (targets.length === 0) {
      setNotice({ severity: 'info', text: 'No node is healthy right now, so nothing was sent. Nodes pick the catalog up on their next sync.' });
      return;
    }
    setPushing(true);
    const results = await Promise.allSettled(targets.map((node) => client.refreshNode(node.nodeId)));
    setPushing(false);
    const failed = results.filter((result) => result.status === 'rejected').length;
    const sent = targets.length - failed;
    setNotice({
      severity: failed > 0 ? 'error' : 'success',
      text: `Sent to ${sent} ${sent === 1 ? 'node' : 'nodes'}${failed > 0 ? `, ${failed} failed` : ''}.`,
    });
  };

  const onApproved = (result: ApproveResponse, version: string) => {
    setApproving(null);
    list.refresh();
    nodes.refresh();
    const queued = result.notified?.queued.length ?? 0;
    setNotice({
      severity: 'success',
      text: `Approved ${version}.${result.supersedes ? ` ${result.supersedes} is superseded.` : ''}${result.notified ? ` Refresh queued on ${queued} ${queued === 1 ? 'node' : 'nodes'}.` : ''}`,
    });
  };

  return (
    <Box component="section" aria-labelledby="catalog-title" sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 1.5, justifyContent: 'space-between' }}>
        <Box>
          <Typography id="catalog-title" variant="h4" component="h1">
            Catalog
          </Typography>
          <Button component={RouterLink} to="/lab" size="small" sx={{ ml: -1 }}>
            Back to the lab
          </Button>
        </Box>
        {canWrite ? (
          <Box sx={{ display: 'flex', gap: 1, flexWrap: 'wrap' }}>
            <Button variant="outlined" startIcon={<DownloadIcon />} onClick={() => setImportOpen(true)}>
              Import release…
            </Button>
            <Button variant="contained" startIcon={<AddIcon />} onClick={startDraft}>
              New draft
            </Button>
          </Box>
        ) : null}
      </Box>

      {list.data ? (
        <Typography color="text.secondary" data-testid="catalog-headline">
          {approvedHeadline(approved, nodesOnIt)}
        </Typography>
      ) : null}

      {list.error ? (
        <Alert
          severity={list.data ? 'warning' : 'error'}
          action={
            <Button color="inherit" size="small" onClick={list.refresh}>
              Try again
            </Button>
          }
        >
          {list.data ? 'Could not refresh the catalog. Showing the last answer.' : 'Could not load the catalog.'}
        </Alert>
      ) : null}

      {notice ? (
        <Alert severity={notice.severity} onClose={() => setNotice(null)} role={notice.severity === 'error' ? 'alert' : 'status'}>
          {notice.text}
        </Alert>
      ) : null}

      {list.loading && !list.data ? (
        <Box sx={{ display: 'grid', placeItems: 'center', minHeight: '30vh' }}>
          <CircularProgress aria-label="Loading the catalog" />
        </Box>
      ) : null}

      {list.data && versions.length === 0 ? (
        <Box sx={{ textAlign: 'center', py: 6 }} data-testid="catalog-empty">
          <Typography variant="h6" component="p">
            No catalog versions yet
          </Typography>
          <Typography color="text.secondary" sx={{ mt: 1 }}>
            {canWrite ? 'Import a release or start a new draft.' : 'An administrator can import the first one.'}
          </Typography>
        </Box>
      ) : null}

      {versions.length > 0 ? (
        <VersionTable
          versions={versions}
          pending={pending}
          canWrite={canWrite}
          pushing={pushing}
          onEdit={(version) => openPanel({ kind: 'editor', source: { kind: 'edit', version } })}
          onLint={(version) => void lintAgain(version)}
          onDiff={(version) => openPanel({ kind: 'diff', version })}
          onPush={() => void pushToNodes()}
        />
      ) : null}

      {panel?.kind === 'editor' && canWrite ? (
        <CatalogEditor
          key={panel.source.kind === 'edit' ? `edit-${panel.source.version}` : `new-${panel.source.baseVersion ?? ''}`}
          client={client}
          source={panel.source}
          versions={versions}
          today={today}
          onSaved={() => list.refresh()}
          onApprove={setApproving}
          onClose={() => openPanel(null)}
          onDirtyChange={onDirtyChange}
        />
      ) : null}
      {panel?.kind === 'diff' ? (
        <DiffFor key={panel.version} client={client} version={panel.version} approvedVersion={approved?.version ?? null} versions={versions} onClose={() => openPanel(null)} />
      ) : null}

      <ApproveDialog client={client} version={approving} onClose={() => setApproving(null)} onApproved={onApproved} />
      <ImportDialog
        client={client}
        open={importOpen}
        onClose={() => setImportOpen(false)}
        onQueued={(tag) => {
          setImportOpen(false);
          setNotice({ severity: 'info', text: `Importing ${tag}. The draft appears in the table when the download finishes.` });
          list.refresh();
        }}
      />
    </Box>
  );
}

/** A diff panel for `version`: against the approved version, or, for the approved row, against the one before it. */
function DiffFor({
  client,
  version,
  approvedVersion,
  versions,
  onClose,
}: {
  client: CatalogApi;
  version: string;
  approvedVersion: string | null;
  versions: readonly CatalogVersionSummary[];
  onClose(): void;
}) {
  const isApproved = version === approvedVersion;
  // `versions` is newest first: the previous version of the approved one is the next row down.
  const index = versions.findIndex((item) => item.version === version);
  const previous = isApproved ? (versions[index + 1]?.version ?? null) : null;
  const base = isApproved ? (previous ?? version) : 'approved';
  const diff = useCatalogDiff(client, version, base);
  const title = isApproved ? `Diff of ${version} against ${previous ?? 'nothing'}` : `Diff of ${version} against ${approvedVersion ?? 'the approved version'}`;
  const emptyNote = isApproved
    ? previous === null
      ? `${version} is the first version, so there is nothing to compare it with.`
      : undefined
    : approvedVersion === null
      ? 'No catalog is approved yet, so there is nothing to compare with.'
      : undefined;
  return (
    <DiffPanel
      title={title}
      loading={diff.loading}
      error={diff.error}
      against={isApproved && previous === null ? null : diff.against}
      emptyNote={emptyNote}
      files={diff.files}
      summary={diff.summary}
      onClose={onClose}
    />
  );
}

export default CatalogPage;
