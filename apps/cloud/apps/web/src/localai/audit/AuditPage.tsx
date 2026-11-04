// The audit trail (`/lab/audit`, lab:admin; docs/specs/cloud-app.md section 3.11): who did what to which node, when.
// Read-only. The filters live in the URL (`?node=&action=&from=&to=&page=`), so a view can be shared and the back
// button works. The page shows what the API answered: the summary and the link are built on the server from a
// whitelist of fields, and nothing here reads or prints raw audit metadata.
import HistoryIcon from '@mui/icons-material/History';
import { Alert, Box, Button, CircularProgress, Pagination, Typography } from '@mui/material';
import { useMemo } from 'react';
import { useSearchParams } from 'react-router-dom';

import { AuditFilters } from './AuditFilters';
import { AuditTable } from './AuditTable';
import { rangeIsBackwards } from './format';
import type { AuditFilterState } from './types';
import { useAudit, useAuditFilterOptions, type FetchAudit, type FetchAuditFilters } from './useAudit';

const DAY = /^\d{4}-\d{2}-\d{2}$/;

/** The filter bar's state from the URL; anything that is not well formed is ignored. */
export function filtersFromParams(params: URLSearchParams): AuditFilterState {
  const page = Number(params.get('page') ?? '1');
  const day = (key: string) => {
    const value = params.get(key) ?? '';
    return DAY.test(value) ? value : '';
  };
  return {
    nodeId: params.get('node') ?? '',
    action: params.get('action') ?? '',
    from: day('from'),
    to: day('to'),
    page: Number.isInteger(page) && page >= 1 ? page : 1,
  };
}

export function paramsFromFilters(filters: AuditFilterState): URLSearchParams {
  const params = new URLSearchParams();
  if (filters.nodeId) params.set('node', filters.nodeId);
  if (filters.action) params.set('action', filters.action);
  if (filters.from) params.set('from', filters.from);
  if (filters.to) params.set('to', filters.to);
  if (filters.page > 1) params.set('page', String(filters.page));
  return params;
}

export interface AuditPageProps {
  /** Replace the transports (tests). */
  fetchAudit?: FetchAudit;
  fetchFilters?: FetchAuditFilters;
  timeZone?: string;
}

export function AuditPage({ fetchAudit, fetchFilters, timeZone }: AuditPageProps) {
  const [params, setParams] = useSearchParams();
  const filters = useMemo(() => filtersFromParams(params), [params]);
  const backwards = rangeIsBackwards(filters);
  const { data, loading, error, refresh } = useAudit(filters, fetchAudit, !backwards);
  const { options, error: optionsError } = useAuditFilterOptions(fetchFilters);
  const actionLabels = useMemo(() => new Map((options?.actions ?? []).map((entry) => [entry.action, entry.label])), [options]);

  const change = (next: AuditFilterState) => setParams(paramsFromFilters(next));
  const showing = data && !backwards ? data : null;

  return (
    <Box component="section" aria-labelledby="audit-title" sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
      <Box>
        <Typography id="audit-title" variant="h4" component="h1">
          Audit trail
        </Typography>
        <Typography color="text.secondary">Who did what to which node, newest first. Read only.</Typography>
      </Box>

      <AuditFilters filters={filters} options={options} onChange={change} />
      {optionsError ? <Alert severity="warning">Could not load the filter choices. You can still read the trail.</Alert> : null}

      {error ? (
        <Alert
          severity={data ? 'warning' : 'error'}
          action={
            <Button color="inherit" size="small" onClick={refresh}>
              Try again
            </Button>
          }
        >
          {data ? 'Could not refresh the audit trail. Showing the last answer.' : 'Could not load the audit trail.'}
        </Alert>
      ) : null}

      {loading && !showing && !backwards ? (
        <Box sx={{ display: 'grid', placeItems: 'center', minHeight: '30vh' }}>
          <CircularProgress aria-label="Loading the audit trail" />
        </Box>
      ) : null}

      {showing && showing.items.length === 0 ? (
        <Box sx={{ textAlign: 'center', py: 6 }} data-testid="audit-empty">
          <HistoryIcon aria-hidden color="disabled" sx={{ fontSize: 40 }} />
          <Typography variant="h6" component="p">
            No events match
          </Typography>
          <Typography color="text.secondary">Try another node, action or date range.</Typography>
        </Box>
      ) : null}

      {showing && showing.items.length > 0 ? (
        <>
          <Typography color="text.secondary" data-testid="audit-count" aria-live="polite">
            {`${showing.total} ${showing.total === 1 ? 'event' : 'events'} · page ${showing.page} of ${showing.totalPages}`}
          </Typography>
          <AuditTable rows={showing.items} actionLabels={actionLabels} timeZone={timeZone} />
          {showing.totalPages > 1 ? (
            <Pagination
              count={showing.totalPages}
              page={Math.min(showing.page, showing.totalPages)}
              onChange={(_event, page) => change({ ...filters, page })}
              color="primary"
              size="small"
              siblingCount={0}
              sx={{ alignSelf: 'center' }}
            />
          ) : null}
        </>
      ) : null}
    </Box>
  );
}

export default AuditPage;
