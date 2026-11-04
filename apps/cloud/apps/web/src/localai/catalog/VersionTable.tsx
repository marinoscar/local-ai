// The versions table (docs/specs/cloud-app.md 3.11): Version, Source, Status, Lint and the row's actions. A viewer
// (lab:read) gets the table and the Diff buttons only; Edit, Lint and Push to nodes are for lab:admin.
import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import ErrorIcon from '@mui/icons-material/Error';
import HistoryIcon from '@mui/icons-material/History';
import EditNoteIcon from '@mui/icons-material/EditNote';
import TaskAltIcon from '@mui/icons-material/TaskAlt';
import { Box, Button, Chip, CircularProgress, Table, TableBody, TableCell, TableHead, TableRow, Typography } from '@mui/material';
import type { ReactElement } from 'react';

import type { CatalogSource, CatalogStatus, CatalogVersionSummary } from '../api/catalog';
import { isChecking } from './useCatalog';

export const SOURCE_LABEL: Record<CatalogSource, string> = { github_release: 'release', editor: 'editor', upload: 'upload' };

const STATUS: Record<CatalogStatus, { color: 'default' | 'success' | 'error' | 'info'; variant: 'filled' | 'outlined'; icon: ReactElement }> = {
  draft: { color: 'default', variant: 'outlined', icon: <EditNoteIcon /> },
  invalid: { color: 'error', variant: 'outlined', icon: <ErrorIcon /> },
  valid: { color: 'info', variant: 'outlined', icon: <TaskAltIcon /> },
  approved: { color: 'success', variant: 'filled', icon: <CheckCircleIcon /> },
  superseded: { color: 'default', variant: 'outlined', icon: <HistoryIcon /> },
};

export function StatusChip({ status }: { status: CatalogStatus }) {
  const style = STATUS[status];
  return <Chip size="small" label={status} color={style.color} variant={style.variant} icon={style.icon} data-testid={`status-${status}`} />;
}

/** The Lint column: a tick, "✖ N errors", "checking…" or "✖ checker failed". The words carry the meaning, not the glyph. */
export function lintCell(version: CatalogVersionSummary, checking: boolean): { text: string; tone: 'ok' | 'bad' | 'wait' } {
  if (checking) return { text: 'checking…', tone: 'wait' };
  if (version.lintError) return { text: '✖ checker failed', tone: 'bad' };
  if (version.status === 'invalid') {
    const n = version.problemCount;
    return { text: n > 0 ? `✖ ${n} ${n === 1 ? 'error' : 'errors'}` : '✖ invalid', tone: 'bad' };
  }
  return { text: '✔', tone: 'ok' };
}

/** Cell border: the row carries it on narrow screens. */
const rowCell = { borderBottomWidth: { xs: 0, sm: '1px' }, borderBottomStyle: 'solid', borderBottomColor: 'divider' } as const;

const visuallyHidden = { position: 'absolute', width: '1px', height: '1px', overflow: 'hidden', clip: 'rect(0 0 0 0)', whiteSpace: 'nowrap' } as const;

export interface VersionTableProps {
  versions: readonly CatalogVersionSummary[];
  /** Versions whose re-lint was asked for: the `lintedAt` the row had then. */
  pending?: Readonly<Record<string, string | null>>;
  canWrite: boolean;
  /** True while "Push to nodes" is running. */
  pushing?: boolean;
  onEdit(version: string): void;
  onLint(version: string): void;
  onDiff(version: string): void;
  onPush(): void;
}

export function VersionTable({ versions, pending = {}, canWrite, pushing = false, onEdit, onLint, onDiff, onPush }: VersionTableProps) {
  return (
    <Box sx={{ overflowX: 'auto' }}>
      <Table size="small" aria-label="Catalog versions">
        <TableHead sx={{ display: { xs: 'none', sm: 'table-header-group' } }}>
          <TableRow>
            <TableCell component="th" scope="col">
              Version
            </TableCell>
            <TableCell component="th" scope="col" sx={{ display: { xs: 'none', sm: 'table-cell' } }}>
              Source
            </TableCell>
            <TableCell component="th" scope="col">
              Status
            </TableCell>
            <TableCell component="th" scope="col">
              Lint
            </TableCell>
            <TableCell component="th" scope="col">
              <Box component="span" sx={visuallyHidden}>
                Actions
              </Box>
            </TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {versions.map((version) => {
            const checking = isChecking(version, pending);
            const lint = lintCell(version, checking);
            const editable = version.status === 'draft' || version.status === 'invalid' || version.status === 'valid';
            return (
              <TableRow
                key={version.version}
                hover
                data-testid={`row-${version.version}`}
                // Under 600 px a row is a small card: version, status and lint on one line, the actions under them.
                sx={{ display: { xs: 'flex', sm: 'table-row' }, flexWrap: 'wrap', alignItems: 'center', borderBottomWidth: { xs: '1px', sm: 0 }, borderBottomStyle: 'solid', borderBottomColor: 'divider', py: { xs: 0.5, sm: 0 } }}
              >
                <TableCell component="th" scope="row" sx={{ fontFamily: 'monospace', whiteSpace: 'nowrap', ...rowCell }}>
                  {version.version}
                </TableCell>
                <TableCell sx={{ display: { xs: 'none', sm: 'table-cell' } }}>{SOURCE_LABEL[version.source]}</TableCell>
                <TableCell sx={rowCell}>
                  <StatusChip status={version.status} />
                </TableCell>
                <TableCell sx={{ whiteSpace: 'nowrap', ...rowCell }}>
                  {lint.tone === 'wait' ? <CircularProgress size={12} sx={{ mr: 0.75 }} aria-hidden="true" /> : null}
                  <Typography component="span" variant="body2" color={lint.tone === 'bad' ? 'error' : 'text.primary'} data-testid={`lint-${version.version}`}>
                    {lint.tone === 'ok' ? (
                      <>
                        <span aria-hidden="true">✔</span>
                        <Box component="span" sx={visuallyHidden}>
                          No problems
                        </Box>
                      </>
                    ) : (
                      lint.text
                    )}
                  </Typography>
                </TableCell>
                <TableCell sx={{ ...rowCell, flexBasis: { xs: '100%', sm: 'auto' }, pt: { xs: 0, sm: undefined } }}>
                  <Box sx={{ display: 'flex', gap: 0.5, flexWrap: 'wrap' }}>
                    {canWrite && editable ? (
                      <Button size="small" onClick={() => onEdit(version.version)} aria-label={`Edit ${version.version}`}>
                        Edit
                      </Button>
                    ) : null}
                    {canWrite && editable ? (
                      <Button size="small" onClick={() => onLint(version.version)} disabled={checking} aria-label={`Lint ${version.version}`}>
                        Lint
                      </Button>
                    ) : null}
                    <Button
                      size="small"
                      onClick={() => onDiff(version.version)}
                      aria-label={version.status === 'approved' ? `Diff ${version.version} against the previous version` : `Diff ${version.version} against approved`}
                    >
                      {version.status === 'approved' ? 'Diff' : 'Diff vs approved'}
                    </Button>
                    {canWrite && version.status === 'approved' ? (
                      <Button size="small" onClick={onPush} disabled={pushing} aria-label={`Push ${version.version} to nodes`}>
                        {pushing ? 'Sending…' : 'Push to nodes'}
                      </Button>
                    ) : null}
                  </Box>
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </Box>
  );
}
