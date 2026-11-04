// The Models page (`/gateway/models`, gateway:use; docs/specs/gateway.md section 3.8): every catalog model with its live
// state, its nodes and its policy (offered on /v1, aliases, on-demand load, preferred nodes). A gateway administrator
// edits a policy in a dialog; everyone else reads. The page lays out what the API computed and never derives a state.
import ArrowBackIcon from '@mui/icons-material/ArrowBack';
import EditIcon from '@mui/icons-material/Edit';
import { Alert, Box, Button, Chip, CircularProgress, Paper, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, Typography, useMediaQuery } from '@mui/material';
import { usePermissions } from '@marinoscar/platform-web/identity/headless';
import { useState } from 'react';
import { Link as RouterLink } from 'react-router-dom';

import { usePolledResource } from '../usePolledResource';
import { ModelPolicyDialog } from './ModelPolicyDialog';
import { listModels as defaultListModels, saveModelPolicy as defaultSave } from './modelsApi';
import { describeAliases, describeNodes, describeOnDemand, describePreferred, describeState } from './modelsFormat';
import type { ModelRow, PolicyInput } from './modelsTypes';

/** How often a visible page refreshes (spec 3.11), so the live state stays current. */
export const MODELS_POLL_MS = 5000;

/**
 * Below 1100 px the table becomes a list of cards. Not one of the shell's five coupled breakpoint gates: the table has seven
 * columns and the navigation rail takes room from 600 px on, so at 800 px the last columns (Prefer, Edit) would scroll out of sight.
 */
const NARROW = '(max-width:1099.95px)';

export interface ModelsPageProps {
  /** Replace the transports (tests). */
  listModels?: () => Promise<ModelRow[]>;
  saveModelPolicy?: (modelId: string, input: PolicyInput) => Promise<unknown>;
}

function StateChip({ row }: { row: ModelRow }) {
  const view = describeState(row.state);
  return (
    <Chip size="small" color={view.color} variant={row.state === 'none' ? 'outlined' : 'filled'} label={view.label} title={view.hint} data-testid={`state-${row.modelId}`} />
  );
}

function OfferedChip({ row }: { row: ModelRow }) {
  return row.policy.enabled ? (
    <Chip size="small" variant="outlined" color="success" label="enabled" />
  ) : (
    <Chip size="small" variant="outlined" label="disabled" />
  );
}

export function ModelsPage({ listModels = defaultListModels, saveModelPolicy = defaultSave }: ModelsPageProps) {
  const { hasPermission } = usePermissions();
  const isAdmin = hasPermission('gateway:admin');
  const { data, loading, error, refresh } = usePolledResource(listModels, MODELS_POLL_MS);
  const compact = useMediaQuery(NARROW);
  const [editing, setEditing] = useState<ModelRow | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const rows = data ?? [];
  const catalogIds = rows.map((row) => row.modelId);

  const edit = (row: ModelRow) => (
    <Button size="small" variant="outlined" startIcon={<EditIcon />} onClick={() => setEditing(row)} aria-label={`Edit the policy of ${row.displayName}`}>
      Edit
    </Button>
  );

  return (
    <Box component="section" aria-labelledby="models-title" sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 1.5, justifyContent: 'space-between' }}>
        <Typography id="models-title" variant="h4" component="h1">
          Models
        </Typography>
        <Button component={RouterLink} to="/gateway/keys" variant="outlined" startIcon={<ArrowBackIcon />}>
          API keys
        </Button>
      </Box>
      <Typography color="text.secondary">
        {isAdmin
          ? 'Which models are offered at /v1, under which names, and whether a request may load one that no node has loaded. A load never replaces a model that is answering.'
          : 'The models the lab offers at /v1 right now. A gateway administrator decides which are offered and how.'}
      </Typography>

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
          {data ? 'Could not refresh the models. Showing the last answer.' : 'Could not load the models.'}
        </Alert>
      ) : null}

      {loading && !data ? (
        <Box sx={{ display: 'grid', placeItems: 'center', minHeight: '30vh' }}>
          <CircularProgress aria-label="Loading the models" />
        </Box>
      ) : null}

      {data && rows.length === 0 ? (
        <Box sx={{ textAlign: 'center', py: 6 }} data-testid="models-empty">
          <Typography variant="h6" component="p">
            No models to show
          </Typography>
          <Typography color="text.secondary">{isAdmin ? 'Approve a catalog on the Lab page, then connect a node.' : 'No model is available right now.'}</Typography>
        </Box>
      ) : null}

      {rows.length > 0 && !compact ? (
        <TableContainer sx={{ border: 1, borderColor: 'divider', borderRadius: 1, maxWidth: '100%' }}>
          <Table size="small" aria-label="Models">
            <TableHead>
              <TableRow>
                <TableCell>Model</TableCell>
                <TableCell>State</TableCell>
                <TableCell>Nodes</TableCell>
                <TableCell>Offered</TableCell>
                <TableCell>Aliases</TableCell>
                <TableCell>On demand</TableCell>
                <TableCell>Prefer</TableCell>
                {isAdmin ? (
                  <TableCell>
                    <Box component="span" sx={{ position: 'absolute', width: 1, height: 1, overflow: 'hidden', clip: 'rect(0 0 0 0)' }}>
                      Actions
                    </Box>
                  </TableCell>
                ) : null}
              </TableRow>
            </TableHead>
            <TableBody>
              {rows.map((row) => (
                <TableRow key={row.modelId} data-testid={`model-${row.modelId}`} data-state={row.state} sx={row.policy.enabled ? undefined : { opacity: 0.7 }}>
                  <TableCell sx={{ overflowWrap: 'anywhere', minWidth: 160 }}>
                    {row.displayName}
                    <Typography component="div" variant="caption" color="text.secondary">
                      <code>{row.modelId}</code>
                    </Typography>
                  </TableCell>
                  <TableCell>
                    <StateChip row={row} />
                  </TableCell>
                  <TableCell sx={{ whiteSpace: 'nowrap' }}>{describeNodes(row)}</TableCell>
                  <TableCell>
                    <OfferedChip row={row} />
                  </TableCell>
                  <TableCell sx={{ overflowWrap: 'anywhere' }}>{describeAliases(row.policy.aliases)}</TableCell>
                  <TableCell sx={{ whiteSpace: 'nowrap' }}>{describeOnDemand(row.policy)}</TableCell>
                  <TableCell sx={{ whiteSpace: 'nowrap' }}>{describePreferred(row.policy.preferredNodes)}</TableCell>
                  {isAdmin ? <TableCell align="right">{edit(row)}</TableCell> : null}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      ) : null}

      {rows.length > 0 && compact ? (
        <Box component="ul" aria-label="Models" sx={{ listStyle: 'none', m: 0, p: 0, display: 'flex', flexDirection: 'column', gap: 1.5 }}>
          {rows.map((row) => (
            <Paper key={row.modelId} component="li" variant="outlined" data-testid={`model-${row.modelId}`} data-state={row.state} sx={{ p: 1.5, opacity: row.policy.enabled ? 1 : 0.7 }}>
              <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: 1 }}>
                <Box sx={{ minWidth: 0 }}>
                  <Typography sx={{ fontWeight: 600, overflowWrap: 'anywhere' }}>{row.displayName}</Typography>
                  <Typography variant="caption" color="text.secondary" sx={{ overflowWrap: 'anywhere' }}>
                    <code>{row.modelId}</code>
                  </Typography>
                </Box>
                <StateChip row={row} />
              </Box>
              <Box component="dl" sx={{ m: 0, mt: 1, display: 'grid', gridTemplateColumns: 'auto 1fr', columnGap: 1.5, rowGap: 0.5, '& dt': { color: 'text.secondary' }, '& dd': { m: 0, overflowWrap: 'anywhere' } }}>
                <dt>Nodes</dt>
                <dd>{describeNodes(row)}</dd>
                <dt>Offered</dt>
                <dd>
                  <OfferedChip row={row} />
                </dd>
                <dt>Aliases</dt>
                <dd>{describeAliases(row.policy.aliases)}</dd>
                <dt>On demand</dt>
                <dd>{describeOnDemand(row.policy)}</dd>
                <dt>Prefer</dt>
                <dd>{describePreferred(row.policy.preferredNodes)}</dd>
              </Box>
              {isAdmin ? <Box sx={{ mt: 1 }}>{edit(row)}</Box> : null}
            </Paper>
          ))}
        </Box>
      ) : null}

      {editing ? (
        <ModelPolicyDialog
          row={editing}
          catalogIds={catalogIds}
          save={saveModelPolicy}
          onSaved={() => {
            setNotice(`Saved the policy of ${editing.displayName}. It applies to the next request.`);
            setEditing(null);
            refresh();
          }}
          onClose={() => setEditing(null)}
        />
      ) : null}
    </Box>
  );
}
