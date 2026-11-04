// The Lab overview (`/lab`, lab:read; docs/specs/cloud-app.md section 3.11): one card per node, header counts and the
// gateway figure. The page only lays out what the API computed; health, routable and ark colours are never derived here.
import AddIcon from '@mui/icons-material/Add';
import HistoryIcon from '@mui/icons-material/History';
import HubIcon from '@mui/icons-material/Hub';
import LibraryBooksIcon from '@mui/icons-material/LibraryBooks';
import { Alert, Box, Button, Chip, CircularProgress, Link, Typography } from '@mui/material';
import { usePermissions } from '@marinoscar/platform-web/identity/headless';
import { NodeCard, type LabSummaryDto } from '@localai/ui';
import { Link as RouterLink } from 'react-router-dom';

import { useLabNodes, type FetchLabNodes } from './useLabNodes';

/** "3 nodes · 2 online · 1 offline mode · 2 alerts" */
export function summaryText(summary: LabSummaryDto): string {
  const plural = (count: number, noun: string) => `${count} ${noun}${count === 1 ? '' : 's'}`;
  return [plural(summary.nodes, 'node'), `${summary.online} online`, `${summary.offlineMode} offline mode`, plural(summary.activeAlerts, 'alert')].join(' · ');
}

export interface LabOverviewPageProps {
  /** Replaces the transport (tests). */
  fetchNodes?: FetchLabNodes;
  /** The instant "last seen" counts from (tests). */
  now?: Date;
  timeZone?: string;
}

export function LabOverviewPage({ fetchNodes, now, timeZone }: LabOverviewPageProps) {
  const { data, loading, error, refresh } = useLabNodes(fetchNodes);
  const { hasPermission } = usePermissions();
  // The Add node button is for lab:admin only; a viewer sees no action buttons (CLOUD-14 builds the wizard it opens).
  const canAdd = hasPermission('lab:admin');

  return (
    <Box component="section" aria-labelledby="lab-title" sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 1.5, justifyContent: 'space-between' }}>
        <Typography id="lab-title" variant="h4" component="h1">
          Lab
        </Typography>
        <Box sx={{ display: 'flex', gap: 1, flexWrap: 'wrap' }}>
          <Button component={RouterLink} to="/lab/catalog" variant="outlined" startIcon={<LibraryBooksIcon />}>
            Catalog
          </Button>
          {canAdd ? (
            <>
              {/* The audit trail is lab:admin only (GET /api/localai/audit), so is its way in. */}
              <Button component={RouterLink} to="/lab/audit" variant="outlined" startIcon={<HistoryIcon />}>
                Audit trail
              </Button>
              <Button component={RouterLink} to="/lab/nodes/new" variant="contained" startIcon={<AddIcon />}>
                Add node
              </Button>
            </>
          ) : null}
        </Box>
      </Box>

      {data ? (
        <Box sx={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 1.5, justifyContent: 'space-between' }} data-testid="lab-summary">
          <Typography color="text.secondary">
            {summaryText(data.summary)}
            {' · '}
            <Link component={RouterLink} to="/lab/alerts">
              View alerts
            </Link>
          </Typography>
          <Chip
            icon={<HubIcon />}
            color={data.summary.routable > 0 ? 'success' : 'default'}
            variant="outlined"
            label={`Gateway: ${data.summary.routable} ${data.summary.routable === 1 ? 'node' : 'nodes'}`}
            data-testid="gateway-chip"
          />
        </Box>
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
          {data ? 'Could not refresh the lab. Showing the last answer.' : 'Could not load the lab.'}
        </Alert>
      ) : null}

      {loading && !data ? (
        <Box sx={{ display: 'grid', placeItems: 'center', minHeight: '30vh' }}>
          <CircularProgress aria-label="Loading the lab" />
        </Box>
      ) : null}

      {data && data.nodes.length === 0 ? (
        <Box sx={{ textAlign: 'center', py: 6 }} data-testid="lab-empty">
          <Typography variant="h6" component="p">
            No nodes yet
          </Typography>
          {canAdd ? (
            <Button component={RouterLink} to="/lab/nodes/new" variant="contained" startIcon={<AddIcon />} sx={{ mt: 2 }}>
              Add node
            </Button>
          ) : (
            <Typography color="text.secondary" sx={{ mt: 1 }}>
              An administrator can add the first node.
            </Typography>
          )}
        </Box>
      ) : null}

      {data && data.nodes.length > 0 ? (
        <Box
          component="ul"
          sx={{ listStyle: 'none', m: 0, p: 0, display: 'grid', gap: 2, gridTemplateColumns: { xs: '1fr', md: 'repeat(2, 1fr)', lg: 'repeat(3, 1fr)' } }}
          aria-label="Nodes"
        >
          {data.nodes.map((node) => (
            <Box component="li" key={node.nodeId} sx={{ minWidth: 0 }}>
              <NodeCard
                node={node}
                now={now}
                timeZone={timeZone}
                actions={
                  <Button component={RouterLink} to={`/lab/nodes/${node.nodeId}`} size="small" variant="outlined" aria-label={`Open ${node.nodeId}`}>
                    Open
                  </Button>
                }
              />
            </Box>
          ))}
        </Box>
      ) : null}
    </Box>
  );
}

export default LabOverviewPage;
