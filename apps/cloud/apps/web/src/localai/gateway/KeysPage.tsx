// The API keys page (`/gateway/keys`, gateway:use; docs/specs/gateway.md section 3.8): the caller's gateway keys with
// prefix, models, limits, expiry and last use; [Create key] and, after the API answers, the show-once panel; [Revoke]
// behind a confirmation. A gateway administrator can also list every user's keys. The page lays out what the API returned
// and never sees a token except inside the Create dialog that the API just answered.
import AddIcon from '@mui/icons-material/Add';
import ModelsIcon from '@mui/icons-material/ViewList';
import { Alert, Box, Button, Checkbox, CircularProgress, FormControlLabel, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, Typography } from '@mui/material';
import { usePermissions } from '@marinoscar/platform-web/identity/headless';
import { formatAgo } from '@localai/ui';
import { useCallback, useEffect, useRef, useState } from 'react';
import { Link as RouterLink } from 'react-router-dom';

import { ConfirmDialog } from '../node/ConfirmDialog';
import { usePolledResource } from '../usePolledResource';
import { CreateKeyDialog } from './CreateKeyDialog';
import { createKey as defaultCreateKey, DEFAULT_KEY_DEFAULTS, fetchKeyDefaults as defaultFetchDefaults, fetchModelSuggestions, listKeys as defaultListKeys, revokeKey as defaultRevokeKey } from './gatewayApi';
import { describeExpiry, describeLimits, describeModels } from './format';
import type { CreatedKey, CreateKeyInput, GatewayKey, KeyDefaults, KeyFilters } from './types';

/** How often a visible page refreshes (spec 3.11), so "last used" stays current. */
export const KEYS_POLL_MS = 5000;

export interface KeysPageProps {
  /** Replace the transports (tests). */
  listKeys?: (filters: KeyFilters) => Promise<GatewayKey[]>;
  createKey?: (input: CreateKeyInput) => Promise<CreatedKey>;
  revokeKey?: (id: string) => Promise<GatewayKey>;
  fetchDefaults?: () => Promise<KeyDefaults>;
  loadSuggestions?: () => Promise<string[]>;
  /** The instant "N min ago" counts from (tests). */
  now?: Date;
  origin?: string;
}

const STATUS_WORD: Record<GatewayKey['status'], string> = { active: 'Active', expired: 'Expired', revoked: 'Revoked' };

export function KeysPage({
  listKeys = defaultListKeys,
  createKey = defaultCreateKey,
  revokeKey = defaultRevokeKey,
  fetchDefaults = defaultFetchDefaults,
  loadSuggestions = fetchModelSuggestions,
  now,
  origin,
}: KeysPageProps) {
  const { hasPermission } = usePermissions();
  const isAdmin = hasPermission('gateway:admin');
  const [filters, setFilters] = useState<KeyFilters>({ all: false, includeRevoked: false });
  const fetcher = useCallback(() => listKeys(filters), [listKeys, filters]);
  const { data, loading, error, refresh } = usePolledResource(fetcher, KEYS_POLL_MS);
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

  const [creating, setCreating] = useState(false);
  const [defaults, setDefaults] = useState<KeyDefaults>(DEFAULT_KEY_DEFAULTS);
  const [revoking, setRevoking] = useState<GatewayKey | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const openCreate = async () => {
    try {
      setDefaults(await fetchDefaults());
    } catch {
      setDefaults(DEFAULT_KEY_DEFAULTS); // the API enforces the real limits and answers with its message
    }
    setCreating(true);
  };

  const confirmRevoke = async (): Promise<string | null> => {
    if (!revoking) return null;
    try {
      await revokeKey(revoking.id);
    } catch (caught) {
      return caught instanceof Error && caught.message ? caught.message : 'Could not revoke the key.';
    }
    setNotice(`Revoked ${revoking.tokenPrefix}… "${revoking.name}". Programs using it stop working on their next request.`);
    setRevoking(null);
    refresh();
    return null;
  };

  const clock = now ?? new Date();
  const keys = data ?? [];
  const showOwner = filters.all;

  return (
    <Box component="section" aria-labelledby="keys-title" sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 1.5, justifyContent: 'space-between' }}>
        <Typography id="keys-title" variant="h4" component="h1">
          API keys
        </Typography>
        <Box sx={{ display: 'flex', gap: 1, flexWrap: 'wrap' }}>
          {/* The other pages of the gateway destination (docs/specs/gateway.md section 3.8). */}
          <Button component={RouterLink} to="/gateway/models" variant="outlined" startIcon={<ModelsIcon />}>
            Models
          </Button>
          <Button variant="contained" startIcon={<AddIcon />} onClick={() => void openCreate()}>
            Create key
          </Button>
        </Box>
      </Box>
      <Typography color="text.secondary">
        A key lets a program use the lab's models at <code>/v1</code>. Make one key per program or computer, and revoke it when you stop using it.
      </Typography>

      {isAdmin ? (
        <Box sx={{ display: 'flex', flexWrap: 'wrap', columnGap: 3 }}>
          <FormControlLabel control={<Checkbox checked={filters.all} onChange={(event) => setFilters((previous) => ({ ...previous, all: event.target.checked }))} />} label="Show every user's keys" />
          <FormControlLabel control={<Checkbox checked={filters.includeRevoked} onChange={(event) => setFilters((previous) => ({ ...previous, includeRevoked: event.target.checked }))} />} label="Show revoked keys" />
        </Box>
      ) : (
        <FormControlLabel control={<Checkbox checked={filters.includeRevoked} onChange={(event) => setFilters((previous) => ({ ...previous, includeRevoked: event.target.checked }))} />} label="Show revoked keys" />
      )}

      {notice ? (
        <Alert severity="success" onClose={() => setNotice(null)}>
          {notice}
        </Alert>
      ) : null}
      {error ? (
        <Alert
          severity={data ? 'warning' : 'error'}
          action={
            <Button color="inherit" size="small" onClick={refresh}>
              Try again
            </Button>
          }
        >
          {data ? 'Could not refresh the keys. Showing the last answer.' : 'Could not load the keys.'}
        </Alert>
      ) : null}

      {loading && !data ? (
        <Box sx={{ display: 'grid', placeItems: 'center', minHeight: '30vh' }}>
          <CircularProgress aria-label="Loading the keys" />
        </Box>
      ) : null}

      {data && keys.length === 0 ? (
        <Box sx={{ textAlign: 'center', py: 6 }} data-testid="keys-empty">
          <Typography variant="h6" component="p">
            No keys yet
          </Typography>
          <Typography color="text.secondary">Press Create key to make your first one.</Typography>
        </Box>
      ) : null}

      {keys.length > 0 ? (
        <TableContainer sx={{ border: 1, borderColor: 'divider', borderRadius: 1, maxWidth: '100%' }}>
          <Table size="small" aria-label="API keys">
            <TableHead>
              <TableRow>
                <TableCell>Name</TableCell>
                {showOwner ? <TableCell>Owner</TableCell> : null}
                <TableCell>Key</TableCell>
                <TableCell>Models</TableCell>
                <TableCell>Limits</TableCell>
                <TableCell>Expires</TableCell>
                <TableCell>Last used</TableCell>
                <TableCell>
                  <Box component="span" sx={{ position: 'absolute', width: 1, height: 1, overflow: 'hidden', clip: 'rect(0 0 0 0)' }}>
                    Actions
                  </Box>
                </TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {keys.map((key) => (
                <TableRow key={key.id} data-testid={`key-${key.id}`} data-status={key.status} sx={key.status === 'active' ? undefined : { opacity: 0.7 }}>
                  <TableCell sx={{ overflowWrap: 'anywhere', minWidth: 120 }}>
                    {key.name}
                    {key.kind === 'service' ? (
                      <Typography component="span" variant="caption" color="text.secondary">
                        {' '}
                        (service)
                      </Typography>
                    ) : null}
                    {key.status !== 'active' ? (
                      <Typography component="span" variant="caption" color="text.secondary" data-testid="key-status">
                        {' '}
                        · {STATUS_WORD[key.status]}
                      </Typography>
                    ) : null}
                  </TableCell>
                  {showOwner ? <TableCell sx={{ overflowWrap: 'anywhere' }}>{key.owner?.email ?? key.ownerUserId}</TableCell> : null}
                  <TableCell>
                    <Box component="code">{key.tokenPrefix}…</Box>
                  </TableCell>
                  <TableCell sx={{ overflowWrap: 'anywhere' }} title={key.allowedModels.join(', ') || 'every model'}>
                    {describeModels(key.allowedModels)}
                  </TableCell>
                  <TableCell sx={{ whiteSpace: 'nowrap' }}>{describeLimits(key.limits)}</TableCell>
                  <TableCell sx={{ whiteSpace: 'nowrap' }}>{describeExpiry(key.expiresAt)}</TableCell>
                  <TableCell sx={{ whiteSpace: 'nowrap' }}>{formatAgo(key.lastUsedAt, clock) ?? 'never'}</TableCell>
                  <TableCell align="right">
                    {key.status !== 'revoked' ? (
                      <Button size="small" color="error" variant="outlined" onClick={() => setRevoking(key)} aria-label={`Revoke ${key.name}`}>
                        Revoke
                      </Button>
                    ) : null}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      ) : null}

      {creating ? (
        <CreateKeyDialog
          canCreateService={isAdmin}
          defaults={defaults}
          createKey={createKey}
          loadSuggestions={loadSuggestions}
          onCreated={refresh}
          onClose={() => setCreating(false)}
          {...(origin !== undefined ? { origin } : {})}
        />
      ) : null}

      <ConfirmDialog
        open={revoking !== null}
        title="Revoke this key?"
        confirmLabel="Revoke"
        destructive
        onConfirm={confirmRevoke}
        onClose={() => setRevoking(null)}
      >
        <Typography>
          {revoking ? (
            <>
              <strong>{revoking.name}</strong> (<code>{revoking.tokenPrefix}…</code>) stops working on the next request. This cannot be undone; make a new key if you still need one.
            </>
          ) : null}
        </Typography>
      </ConfirmDialog>
    </Box>
  );
}
