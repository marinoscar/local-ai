// The line diff of one version against another, as text: per file a header (path, what happened, +/- counts) and
// unified hunks. Colour is never the only signal: every line starts with its `+`, `-` or space, and a file's change
// is a word. Unchanged files are listed, not expanded.
import { Alert, Box, Button, CircularProgress, Typography } from '@mui/material';

import type { FileLineChanges } from './catalogDiff';
import { summaryText, type DiffSummary } from './catalogDiff';
import { hunkHeader } from './lineDiff';

const CHANGE_WORD: Record<FileLineChanges['change'], string> = {
  added: 'added',
  removed: 'removed',
  changed: 'changed',
  unchanged: 'unchanged',
};

export interface DiffViewProps {
  files: readonly FileLineChanges[];
  summary: DiffSummary;
}

export function DiffView({ files, summary }: DiffViewProps) {
  const changed = files.filter((file) => file.change !== 'unchanged');
  const unchanged = files.filter((file) => file.change === 'unchanged');
  return (
    <Box data-testid="diff-view" sx={{ display: 'flex', flexDirection: 'column', gap: 1.5 }}>
      <Typography variant="body2" data-testid="diff-summary">
        {summaryText(summary)}
      </Typography>
      {changed.map((file) => (
        <Box component="section" key={file.path} aria-label={`Changes in ${file.path}`} sx={{ border: 1, borderColor: 'divider', borderRadius: 1, overflow: 'hidden' }}>
          <Box sx={{ display: 'flex', gap: 1, alignItems: 'baseline', flexWrap: 'wrap', px: 1.5, py: 0.75, bgcolor: 'action.hover' }}>
            <Typography component="h3" variant="subtitle2" sx={{ fontFamily: 'monospace' }}>
              {file.path}
            </Typography>
            <Typography variant="caption" color="text.secondary">
              {CHANGE_WORD[file.change]} · +{file.added} -{file.removed}
            </Typography>
          </Box>
          <Box sx={{ overflowX: 'auto' }} tabIndex={0} role="group" aria-label={`Diff of ${file.path}`}>
            {file.hunks.map((hunk) => (
              <Box
                component="pre"
                key={`${hunk.oldStart}-${hunk.newStart}`}
                sx={{ m: 0, py: 0.5, fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace', fontSize: 13, lineHeight: '20px', minWidth: 'max-content' }}
              >
                <Box component="span" sx={{ display: 'block', px: 1.5, color: 'text.secondary' }}>
                  {hunkHeader(hunk)}
                </Box>
                {hunk.lines.map((line, index) => (
                  <Box
                    component="span"
                    key={index}
                    data-kind={line.kind}
                    sx={{
                      display: 'block',
                      px: 1.5,
                      bgcolor: line.kind === 'add' ? 'success.light' : line.kind === 'remove' ? 'error.light' : 'transparent',
                      color: line.kind === 'equal' ? 'text.primary' : (theme) => theme.palette.getContrastText(line.kind === 'add' ? theme.palette.success.light : theme.palette.error.light),
                    }}
                  >
                    {line.kind === 'add' ? '+' : line.kind === 'remove' ? '-' : ' '}
                    {line.text}
                  </Box>
                ))}
              </Box>
            ))}
          </Box>
        </Box>
      ))}
      {unchanged.length > 0 ? (
        <Typography variant="caption" color="text.secondary">
          Unchanged: {unchanged.map((file) => file.path).join(', ')}
        </Typography>
      ) : null}
    </Box>
  );
}

export interface DiffPanelProps {
  title: string;
  loading: boolean;
  error: Error | null;
  /** The version compared against, null when there is none. */
  against: string | null;
  /** A sentence when there is nothing to compare with. */
  emptyNote?: string;
  files: readonly FileLineChanges[];
  summary: DiffSummary;
  onClose(): void;
}

/** The panel the Catalog page opens for "Diff": a heading, the state of the request, and the diff. */
export function DiffPanel({ title, loading, error, against, emptyNote, files, summary, onClose }: DiffPanelProps) {
  return (
    <Box component="section" aria-label={title} data-testid="diff-panel" sx={{ display: 'flex', flexDirection: 'column', gap: 1.5, border: 1, borderColor: 'divider', borderRadius: 1, p: 2 }}>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 1, flexWrap: 'wrap' }}>
        <Typography variant="h6" component="h2">
          {title}
        </Typography>
        <Button size="small" onClick={onClose}>
          Close diff
        </Button>
      </Box>
      {loading ? <CircularProgress size={24} aria-label="Loading the diff" /> : null}
      {error ? <Alert severity="error">Could not load the diff. Close it and try again.</Alert> : null}
      {!loading && !error && against === null && emptyNote ? <Alert severity="info">{emptyNote}</Alert> : null}
      {!loading && !error && (against !== null || !emptyNote) ? <DiffView files={files} summary={summary} /> : null}
    </Box>
  );
}
