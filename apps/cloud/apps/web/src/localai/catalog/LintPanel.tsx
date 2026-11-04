// The lint problems of one version: `CODE file:path message`, one button per problem (click = jump to the file and,
// best effort, the line). The sentences are the cloud's own; the codes (LAI-CAT-0xx) come from `aictl catalog lint`
// and are registered in docs/specs/doctor-codes.md.
import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import ErrorIcon from '@mui/icons-material/Error';
import { Alert, Box, ButtonBase, CircularProgress, Typography } from '@mui/material';

import type { CatalogStatus, LintProblem, LintResult } from '../api/catalog';

/** The doctor code of "the catalog checker did not run" (docs/specs/doctor-codes.md). */
export const CHECKER_FAILED_CODE = 'LAI-CLD-033';

export interface LintPanelProps {
  /** The version these problems belong to (shown in the heading). */
  version: string;
  status: CatalogStatus;
  /** Null while the lint has not produced a verdict. */
  lintedAt: string | null;
  lintResult: LintResult | null;
  /** True when the textarea holds edits the saved version does not have, so the verdict is about the old text. */
  stale?: boolean;
  onSelect?(problem: LintProblem): void;
}

/** "LAI-CAT-011 models.yaml:models[3].source.revision must be a 40-hex commit" */
export function problemLabel(problem: LintProblem): string {
  const where = problem.path ? `${problem.file}:${problem.path}` : problem.file;
  return `${problem.code} ${where} ${problem.message}`;
}

export function LintPanel({ version, status, lintedAt, lintResult, stale = false, onSelect }: LintPanelProps) {
  const problems = lintResult?.problems ?? [];
  const failure = lintResult?.error ?? null;

  let body;
  if (lintedAt === null) {
    body = (
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }} role="status">
        <CircularProgress size={16} aria-hidden="true" />
        <Typography variant="body2">Checking the catalog…</Typography>
      </Box>
    );
  } else if (failure) {
    body = (
      <Alert severity="error" role="alert">
        The catalog checker failed ({CHECKER_FAILED_CODE}). Admin: redeploy the cloud app.
      </Alert>
    );
  } else if (problems.length > 0) {
    body = (
      <Box component="ul" sx={{ listStyle: 'none', m: 0, p: 0, display: 'flex', flexDirection: 'column', gap: 0.5 }} aria-label={`Lint problems in ${version}`}>
        {problems.map((problem, index) => (
          <li key={`${problem.code}-${problem.file}-${problem.path}-${index}`}>
            <ButtonBase
              onClick={() => onSelect?.(problem)}
              sx={{
                display: 'flex',
                alignItems: 'flex-start',
                justifyContent: 'flex-start',
                gap: 1,
                width: '100%',
                textAlign: 'left',
                p: 0.75,
                borderRadius: 1,
                '&:hover': { bgcolor: 'action.hover' },
                '&.Mui-focusVisible': { outline: '2px solid', outlineColor: 'primary.main' },
              }}
              aria-label={problemLabel(problem)}
            >
              <ErrorIcon color="error" fontSize="small" aria-hidden="true" sx={{ mt: '2px', flex: 'none' }} />
              <Typography component="span" variant="body2" sx={{ fontFamily: 'monospace', overflowWrap: 'anywhere' }}>
                <strong>{problem.code}</strong> {problem.path ? `${problem.file}:${problem.path}` : problem.file} {problem.message}
              </Typography>
            </ButtonBase>
          </li>
        ))}
      </Box>
    );
  } else {
    body = (
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }} role="status">
        <CheckCircleIcon color="success" fontSize="small" aria-hidden="true" />
        <Typography variant="body2">{status === 'invalid' ? 'The checker reported the catalog as invalid.' : 'No problems found.'}</Typography>
      </Box>
    );
  }

  return (
    <Box component="section" aria-label={`Lint result for ${version}`} data-testid="lint-panel" sx={{ border: 1, borderColor: 'divider', borderRadius: 1, p: 1.5 }}>
      <Typography variant="subtitle2" component="h3" sx={{ mb: 1 }}>
        {problems.length > 0 && lintedAt !== null ? `Lint problems (${problems.length})` : 'Lint'}
      </Typography>
      {stale ? (
        <Typography variant="caption" color="text.secondary" component="p" sx={{ mb: 1 }}>
          You changed the text after {version} was saved. Save the draft to check the new text.
        </Typography>
      ) : null}
      {body}
    </Box>
  );
}
