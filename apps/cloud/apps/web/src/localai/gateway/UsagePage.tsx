// The Usage page (`/gateway/usage`, gateway:use; docs/specs/gateway.md section 3.8): requests and tokens of your API keys
// over the last 7 or 30 UTC days, by day, by model or by node, with a bar per row. A gateway administrator can switch to
// every user's keys. The model and node views read the per-request rows, which the API only answers for 7 days, so they
// are offered for the 7-day range. The page lays out what the API returned; it never sees a prompt, only counts.
import { Alert, Box, Button, Checkbox, CircularProgress, FormControlLabel, MenuItem, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, TextField, Typography } from '@mui/material';
import { usePermissions } from '@marinoscar/platform-web/identity/headless';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { GatewayNav } from './GatewayNav';
import { listKeys as defaultListKeys } from './gatewayApi';
import type { GatewayKey, KeyFilters } from './types';
import { fetchUsage as defaultFetchUsage } from './usageApi';
import { barPercent, describeKey, describeNodeShares, describeRequests, describeTokens, describeTokenSplit } from './usageFormat';
import type { UsageFilters, UsageGroupBy, UsageResponse } from './usageTypes';
import { usePolledResource } from '../usePolledResource';

/** How often a visible page refreshes: the recorder writes every 2 s, so a minute is plenty for a report. */
export const USAGE_POLL_MS = 30_000;

export interface UsagePageProps {
  /** Replace the transports (tests). */
  fetchUsage?: (filters: UsageFilters, now: Date) => Promise<UsageResponse>;
  listKeys?: (filters: KeyFilters) => Promise<GatewayKey[]>;
  /** The instant "last N days" counts back from (tests). */
  now?: Date;
}

const GROUP_LABEL: Record<UsageGroupBy, string> = { day: 'Day', model: 'Model', node: 'Node' };
const TITLE: Record<UsageGroupBy, string> = { day: 'Usage by day', model: 'Usage by model', node: 'Usage by node' };

// Pixels, not `1`: in `sx` a bare 1 is 100%, which would stretch the hidden node across the page.
const visuallyHidden = { position: 'absolute', width: '1px', height: '1px', overflow: 'hidden', clip: 'rect(0 0 0 0)' } as const;

export function UsagePage({ fetchUsage = defaultFetchUsage, listKeys = defaultListKeys, now }: UsagePageProps) {
  const { hasPermission } = usePermissions();
  const isAdmin = hasPermission('gateway:admin');
  const [filters, setFilters] = useState<UsageFilters>({ days: 30, groupBy: 'day', keyId: null, all: false });

  const fetcher = useCallback(() => fetchUsage(filters, now ?? new Date()), [fetchUsage, filters, now]);
  const { data, loading, error, refresh } = usePolledResource(fetcher, USAGE_POLL_MS);
  // The poller keeps its first fetcher; ask for a fresh read whenever the filters change.
  const firstRun = useRef(true);
  useEffect(() => {
    if (firstRun.current) {
      firstRun.current = false; // the poller already made the first read
      return;
    }
    refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filters]);

  const keysFetcher = useCallback(() => listKeys({ all: filters.all, includeRevoked: true }), [listKeys, filters.all]);
  const [keys, setKeys] = useState<GatewayKey[]>([]);
  useEffect(() => {
    let live = true;
    keysFetcher().then(
      (next) => live && setKeys(next),
      () => live && setKeys([]), // the key filter is a convenience: without it "all keys" still works
    );
    return () => {
      live = false;
    };
  }, [keysFetcher]);

  const setDays = (days: 7 | 30) => setFilters((previous) => ({ ...previous, days, groupBy: days === 30 ? 'day' : previous.groupBy }));
  const rows = useMemo(() => {
    if (!data) return [];
    // Days newest first; models and nodes arrive busiest first.
    return data.groupBy === 'day' ? [...data.rows].reverse() : data.rows;
  }, [data]);
  const max = rows.reduce((top, row) => Math.max(top, row.requests), 0);
  const shownGroup = data?.groupBy ?? filters.groupBy;
  const empty = data !== null && data.totals.requests === 0;

  return (
    <Box component="section" aria-labelledby="usage-title" sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
      <GatewayNav />
      <Typography id="usage-title" variant="h4" component="h1">
        Usage
      </Typography>
      <Typography color="text.secondary">
        What your API keys used over the last {filters.days} days (UTC days). A request counts on the day it finishes; tokens are what the lab reported for it.
      </Typography>

      <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 2, alignItems: 'flex-start' }}>
        <TextField select size="small" label="Range" value={filters.days} onChange={(event) => setDays(Number(event.target.value) === 7 ? 7 : 30)} sx={{ minWidth: 160 }}>
          <MenuItem value={7}>Last 7 days</MenuItem>
          <MenuItem value={30}>Last 30 days</MenuItem>
        </TextField>
        <TextField
          select
          size="small"
          label="Group by"
          value={filters.groupBy}
          onChange={(event) => setFilters((previous) => ({ ...previous, groupBy: event.target.value as UsageGroupBy }))}
          helperText={filters.days === 30 ? 'Model and node views cover 7 days.' : undefined}
          sx={{ minWidth: 160 }}
        >
          <MenuItem value="day">Day</MenuItem>
          <MenuItem value="model" disabled={filters.days === 30}>
            Model
          </MenuItem>
          <MenuItem value="node" disabled={filters.days === 30}>
            Node
          </MenuItem>
        </TextField>
        <TextField
          select
          size="small"
          label="Key"
          value={filters.keyId ?? ''}
          onChange={(event) => setFilters((previous) => ({ ...previous, keyId: event.target.value === '' ? null : event.target.value }))}
          sx={{ minWidth: 200, maxWidth: '100%' }}
        >
          <MenuItem value="">{filters.all ? 'All keys' : 'All my keys'}</MenuItem>
          {keys.map((key) => (
            <MenuItem key={key.id} value={key.id}>
              {key.name} ({key.tokenPrefix}…)
            </MenuItem>
          ))}
        </TextField>
        {isAdmin ? (
          <FormControlLabel
            control={<Checkbox checked={filters.all} onChange={(event) => setFilters((previous) => ({ ...previous, all: event.target.checked, keyId: null }))} />}
            label="Show every user's keys"
          />
        ) : null}
      </Box>

      {error ? (
        <Alert
          severity={data ? 'warning' : 'error'}
          action={
            <Button color="inherit" size="small" onClick={refresh}>
              Try again
            </Button>
          }
        >
          {data ? 'Could not refresh the usage. Showing the last answer.' : 'Could not load the usage.'}
        </Alert>
      ) : null}

      {loading && !data ? (
        <Box sx={{ display: 'grid', placeItems: 'center', minHeight: '30vh' }}>
          <CircularProgress aria-label="Loading the usage" />
        </Box>
      ) : null}

      {data ? (
        <Box component="dl" data-testid="usage-totals" sx={{ display: 'flex', flexWrap: 'wrap', gap: 3, m: 0 }}>
          <Total label="Requests" value={data.totals.requests.toLocaleString('en-US')} />
          <Total label="Prompt tokens" value={data.totals.promptTokens.toLocaleString('en-US')} />
          <Total label="Completion tokens" value={data.totals.completionTokens.toLocaleString('en-US')} />
        </Box>
      ) : null}

      {empty ? (
        <Box sx={{ textAlign: 'center', py: 6 }} data-testid="usage-empty">
          <Typography variant="h6" component="p">
            No usage in this period
          </Typography>
          <Typography color="text.secondary">Requests appear here a few seconds after they finish.</Typography>
        </Box>
      ) : null}

      {data && !empty ? (
        <TableContainer sx={{ border: 1, borderColor: 'divider', borderRadius: 1, maxWidth: '100%' }}>
          <Table size="small" aria-label={TITLE[shownGroup]}>
            <TableHead>
              <TableRow>
                <TableCell>{GROUP_LABEL[shownGroup]}</TableCell>
                <TableCell sx={{ width: '45%' }}>Requests</TableCell>
                <TableCell align="right">Tokens</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {rows.map((row) => {
                const shares = describeNodeShares(row.nodes);
                return (
                  <TableRow key={row.key ?? '(none)'} data-testid={`usage-row-${row.key ?? 'none'}`}>
                    <TableCell sx={{ overflowWrap: 'anywhere', verticalAlign: 'top' }}>
                      <Box component="span" sx={{ whiteSpace: 'nowrap' }}>
                        {describeKey(row.key)}
                      </Box>
                      {shares ? (
                        <Typography component="div" variant="caption" color="text.secondary" data-testid="usage-shares">
                          {shares}
                        </Typography>
                      ) : null}
                    </TableCell>
                    <TableCell sx={{ verticalAlign: 'top' }}>
                      <Box aria-hidden sx={{ height: 8, borderRadius: 1, bgcolor: 'action.hover', mb: 0.5 }}>
                        <Box sx={{ height: 1, borderRadius: 1, bgcolor: 'primary.main', width: `${barPercent(row.requests, max)}%` }} />
                      </Box>
                      {describeRequests(row.requests)}
                    </TableCell>
                    <TableCell align="right" sx={{ whiteSpace: 'nowrap', verticalAlign: 'top' }}>
                      {describeTokens(row)}
                      <Typography component="div" variant="caption" color="text.secondary">
                        {describeTokenSplit(row)}
                      </Typography>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </TableContainer>
      ) : null}
      <Box component="span" sx={visuallyHidden} role="status">
        {data ? `${describeRequests(data.totals.requests)} from ${data.from} to ${data.to}` : ''}
      </Box>
    </Box>
  );
}

function Total({ label, value }: { label: string; value: string }) {
  return (
    <Box>
      <Typography component="dt" variant="body2" color="text.secondary">
        {label}
      </Typography>
      <Typography component="dd" variant="h5" sx={{ m: 0 }}>
        {value}
      </Typography>
    </Box>
  );
}
