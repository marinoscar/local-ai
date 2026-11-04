// The node detail page (`/lab/nodes/:nodeId`, lab:read; docs/specs/cloud-app.md section 3.11, issue #86): where an
// operator acts on ONE node. A header, the commands in flight or just settled, and six tabs (Models, Ark, Services,
// Logs, Keys, Settings). The page reads `GET /api/localai/nodes/:nodeId` every 5 s while it is visible; every action is
// a closed-type command queued through the command API and polled every 2 s. It never talks to a node, derives no
// health and shows no key material.
import { Alert, Box, Button, CircularProgress, Tab, Tabs, Typography } from '@mui/material';
import { ApiError } from '@marinoscar/platform-web/core';
import { Link as RouterLink, useParams, useSearchParams } from 'react-router-dom';

import { getNodeDetail } from '../api/nodes';
import type { NodeDetail } from '../api/types';
import { usePolledResource } from '../usePolledResource';
import { CommandStatusLine } from './CommandStatusLine';
import { NodeHeader } from './NodeHeader';
import { ArkTab } from './tabs/ArkTab';
import { KeysTab } from './tabs/KeysTab';
import { LogsTab } from './tabs/LogsTab';
import { ModelsTab } from './tabs/ModelsTab';
import { ServicesTab } from './tabs/ServicesTab';
import { SettingsTab } from './tabs/SettingsTab';
import { NodeCommandsProvider, useNodeCommands } from './useNodeCommands';
import { requiredPermission } from './commandPolicy';
import { usePermissions } from '@marinoscar/platform-web/identity/headless';

/** How often a visible page refreshes the node (spec 3.11). */
export const NODE_POLL_MS = 5000;

export const TABS = ['models', 'ark', 'services', 'logs', 'keys', 'settings'] as const;
export type TabKey = (typeof TABS)[number];
const TAB_LABEL: Record<TabKey, string> = { models: 'Models', ark: 'Ark', services: 'Services', logs: 'Logs', keys: 'Keys', settings: 'Settings' };

function CommandPanel() {
  const commands = useNodeCommands();
  const { hasPermission } = usePermissions();
  return (
    <>
      {commands.notice ? (
        <Alert severity="warning" role="alert" onClose={commands.clearNotice} data-testid="command-notice">
          {commands.notice}
        </Alert>
      ) : null}
      {commands.commands.length > 0 ? (
        <Box component="section" aria-label="Commands" sx={{ border: 1, borderColor: 'divider', borderRadius: 1, px: 1.5, py: 0.5 }}>
          <Box component="ul" sx={{ m: 0, p: 0 }} aria-live="polite">
            {commands.commands.map((command) => (
              <CommandStatusLine
                key={command.id}
                command={command}
                canCancel={hasPermission(requiredPermission(command.type))}
                onCancel={(id) => void commands.cancel(id)}
                onDismiss={commands.dismiss}
              />
            ))}
          </Box>
        </Box>
      ) : null}
    </>
  );
}

function TabPanel({ node, tab, onChanged, now }: { node: NodeDetail; tab: TabKey; onChanged: () => void; now: Date }) {
  switch (tab) {
    case 'models':
      return <ModelsTab node={node} />;
    case 'ark':
      return <ArkTab node={node} now={now} />;
    case 'services':
      return <ServicesTab node={node} now={now} />;
    case 'logs':
      return <LogsTab node={node} />;
    case 'keys':
      return <KeysTab node={node} now={now} onChanged={onChanged} />;
    case 'settings':
      return <SettingsTab node={node} onChanged={onChanged} />;
  }
}

function NodeDetailView({ nodeId, now, commandPollMs }: { nodeId: string; now?: Date; commandPollMs?: number }) {
  const detail = usePolledResource(() => getNodeDetail(nodeId), NODE_POLL_MS);
  const [params, setParams] = useSearchParams();
  const requested = params.get('tab');
  const tab: TabKey = (TABS as readonly string[]).includes(requested ?? '') ? (requested as TabKey) : 'models';

  if (detail.loading && !detail.data) {
    return (
      <Box sx={{ display: 'grid', placeItems: 'center', minHeight: '30vh' }}>
        <CircularProgress aria-label="Loading the node" />
      </Box>
    );
  }
  if (!detail.data) {
    const missing = detail.error instanceof ApiError && detail.error.status === 404;
    return (
      <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2, alignItems: 'flex-start' }}>
        <Alert
          severity={missing ? 'info' : 'error'}
          action={
            missing ? undefined : (
              <Button color="inherit" size="small" onClick={detail.refresh}>
                Try again
              </Button>
            )
          }
        >
          {missing ? `There is no node ${nodeId}. It may have been retired.` : 'Could not load the node.'}
        </Alert>
        <Button component={RouterLink} to="/lab" variant="outlined">
          Back to the lab
        </Button>
      </Box>
    );
  }

  const node = detail.data;
  const clock = now ?? new Date();
  return (
    <NodeCommandsProvider nodeId={nodeId} onSettled={() => detail.refresh()} pollMs={commandPollMs}>
      <Box component="section" aria-labelledby="node-title" sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
        <NodeHeader node={node} now={clock} />
        {detail.error ? (
          <Alert severity="warning" action={<Button color="inherit" size="small" onClick={detail.refresh}>Try again</Button>}>
            Could not refresh the node. Showing the last answer.
          </Alert>
        ) : null}
        <CommandPanel />
        <Box>
          <Tabs value={tab} onChange={(_event, value: TabKey) => setParams({ tab: value }, { replace: true })} variant="scrollable" scrollButtons="auto" aria-label="Node sections">
            {TABS.map((key) => (
              <Tab key={key} value={key} label={TAB_LABEL[key]} id={`node-tab-${key}`} aria-controls={`node-panel-${key}`} />
            ))}
          </Tabs>
          <Box role="tabpanel" id={`node-panel-${tab}`} aria-labelledby={`node-tab-${tab}`} sx={{ pt: 2 }}>
            <TabPanel node={node} tab={tab} onChanged={detail.refresh} now={clock} />
          </Box>
        </Box>
      </Box>
    </NodeCommandsProvider>
  );
}

export function NodeDetailPage({ now, commandPollMs }: { now?: Date; commandPollMs?: number }) {
  const { nodeId } = useParams<{ nodeId: string }>();
  if (!nodeId) return <Typography>No node selected.</Typography>;
  return <NodeDetailView key={nodeId} nodeId={nodeId} now={now} commandPollMs={commandPollMs} />;
}

export default NodeDetailPage;
