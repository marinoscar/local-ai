// The Lab tab (docs/specs/desktop.md 4.8): the same node cards as the cloud overview (`@localai/ui`), a banner when the cloud
// cannot be reached, and the everyday command buttons. All data comes through `window.localaiDesktop.cloud`; this page makes
// no network request of its own and never sees the token. What to fetch and when is the controller's (model.ts).
import { Alert, Box, Button, Chip, Dialog, DialogActions, DialogContent, DialogContentText, DialogTitle, Menu, MenuItem, Stack, Typography } from '@mui/material';
import { NodeCard } from '@localai/ui';
import type { NodeCardDto } from '@localai/ui/lab/types';
import { type ReactElement, useEffect, useMemo, useState, useSyncExternalStore } from 'react';
import type { Lang } from '../../shared/types';
import { LabController, type CommandSpec, type LabBridge, type LabState } from './model';
import { STRINGS, fill, pick } from './strings';

export interface LabPageProps {
  bridge?: LabBridge;
  lang: Lang;
}

const SERVICES = ['llama', 'kiwix', 'whisper'] as const;

export function LabPage({ bridge, lang }: LabPageProps): ReactElement {
  const controller = useMemo(() => (bridge ? new LabController(bridge, lang) : null), [bridge, lang]);
  useEffect(() => {
    if (!controller) return undefined;
    void controller.start();
    const onVisibility = (): void => controller.setDocumentVisible(document.visibilityState !== 'hidden');
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      document.removeEventListener('visibilitychange', onVisibility);
      controller.stop();
    };
  }, [controller]);

  if (!controller) {
    return (
      <Box component="main" sx={{ p: 3 }}>
        <Typography>{pick(STRINGS.notConnected, lang)}</Typography>
      </Box>
    );
  }
  return <LabView controller={controller} lang={lang} />;
}

function LabView({ controller, lang }: { controller: LabController; lang: Lang }): ReactElement {
  const state = useSyncExternalStore(controller.subscribe, controller.getState);
  const [restart, setRestart] = useState<{ nodeId: string; service: string } | null>(null);
  const banner = controller.bannerText();
  const canAct = controller.canAct();
  const summary = state.summary;

  return (
    <Box component="main" sx={{ p: 2, display: 'flex', flexDirection: 'column', gap: 2, boxSizing: 'border-box', minHeight: '100vh' }} data-testid="lab-page">
      <Stack direction="row" spacing={2} sx={{ alignItems: 'baseline', flexWrap: 'wrap' }}>
        <Typography component="h1" variant="h5" sx={{ fontWeight: 700 }}>
          {pick(STRINGS.title, lang)}
        </Typography>
        {state.user !== null ? (
          <Typography variant="body2" color="text.secondary" data-testid="lab-user">
            {fill(pick(STRINGS.connectedAs, lang), { name: state.user })}
          </Typography>
        ) : state.connected ? (
          <Typography variant="body2" color="text.secondary" data-testid="lab-user">
            {pick(STRINGS.connected, lang)}
          </Typography>
        ) : null}
        {summary !== null ? (
          <Typography variant="body2" color="text.secondary" data-testid="lab-summary">
            {fill(pick(STRINGS.summary, lang), { nodes: String(summary.nodes), online: String(summary.online), alerts: String(summary.activeAlerts) })}
          </Typography>
        ) : null}
      </Stack>

      {banner !== null ? (
        <Alert
          severity="info"
          role="status"
          data-testid="lab-banner"
          action={
            <Button color="inherit" size="small" onClick={() => void controller.refresh()} data-testid="lab-retry">
              {pick(STRINGS.tryNow, lang)}
            </Button>
          }
        >
          <span data-testid="lab-banner-text">{banner}</span> <span>{pick(STRINGS.retrying, lang)}</span> <span>(LAI-DSK-010)</span>
        </Alert>
      ) : null}
      {state.problem !== null ? (
        <Alert severity="warning" data-testid="lab-problem">
          {pick(state.problem === 'forbidden' ? STRINGS.forbidden : state.problem === 'removed' ? STRINGS.removed : STRINGS.unexpected, lang)}
        </Alert>
      ) : null}
      {!state.connected && state.nodes.length === 0 ? <Typography data-testid="lab-not-connected">{pick(STRINGS.notConnected, lang)}</Typography> : null}
      {state.connected && !state.loaded ? <Typography data-testid="lab-loading">{pick(STRINGS.loading, lang)}</Typography> : null}
      {state.loaded && state.nodes.length === 0 && state.problem === null && !state.unreachable ? <Typography data-testid="lab-empty">{pick(STRINGS.empty, lang)}</Typography> : null}

      <Box sx={{ display: 'grid', gap: 2, gridTemplateColumns: 'repeat(auto-fill, minmax(340px, 1fr))', alignItems: 'start' }}>
        {state.nodes.map((node) => (
          <NodeCard key={node.nodeId} node={node} actions={<NodeActions controller={controller} state={state} node={node} lang={lang} canAct={canAct} onRestart={(service) => setRestart({ nodeId: node.nodeId, service })} />} />
        ))}
      </Box>

      <Dialog open={restart !== null} onClose={() => setRestart(null)} aria-labelledby="restart-title">
        <DialogTitle id="restart-title">{restart ? fill(pick(STRINGS.restartTitle, lang), { service: restart.service, node: restart.nodeId }) : ''}</DialogTitle>
        <DialogContent>
          <DialogContentText>{pick(STRINGS.restartBody, lang)}</DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setRestart(null)}>{pick(STRINGS.cancel, lang)}</Button>
          <Button
            variant="contained"
            data-testid="restart-confirm"
            onClick={() => {
              if (restart) void controller.send(restart.nodeId, { type: 'service.restart', params: { service: restart.service }, param: restart.service });
              setRestart(null);
            }}
          >
            {pick(STRINGS.restart, lang)}
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}

interface NodeActionsProps {
  controller: LabController;
  state: LabState;
  node: NodeCardDto;
  lang: Lang;
  canAct: boolean;
  onRestart(service: string): void;
}

/** The footer of one card: Open in browser, the command buttons, the command's line and the models panel. */
function NodeActions({ controller, state, node, lang, canAct, onRestart }: NodeActionsProps): ReactElement {
  const id = node.nodeId;
  const busy = controller.isBusy(id);
  const enabled = canAct && !busy && node.phase === 'enrolled';
  const line = state.commands[id];
  const panel = state.models[id];
  const [menu, setMenu] = useState<HTMLElement | null>(null);
  const send = (spec: CommandSpec): void => void controller.send(id, spec);

  return (
    <Stack spacing={1} sx={{ width: '100%' }}>
      <Stack direction="row" sx={{ flexWrap: 'wrap', gap: 1 }}>
        <Button size="small" variant="outlined" onClick={() => controller.openNode(id)} disabled={!state.connected} data-testid={`open-${id}`}>
          {pick(STRINGS.openInBrowser, lang)}
        </Button>
        <Button size="small" variant="outlined" disabled={!enabled} onClick={() => send({ type: 'status.refresh', params: {} })} data-testid={`refresh-${id}`}>
          {pick(STRINGS.refreshStatus, lang)}
        </Button>
        <Button size="small" variant="outlined" disabled={!canAct || node.phase !== 'enrolled'} onClick={() => controller.toggleModels(id)} data-testid={`models-${id}`}>
          {pick(panel?.open ? STRINGS.hideModels : STRINGS.models, lang)}
        </Button>
        <Button size="small" variant="outlined" disabled={!enabled} onClick={() => send({ type: 'ark.sync', params: {} })} data-testid={`sync-${id}`}>
          {pick(STRINGS.syncNow, lang)}
        </Button>
        <Button size="small" variant="outlined" disabled={!enabled} onClick={() => send({ type: 'ark.verify', params: {} })} data-testid={`verify-${id}`}>
          {pick(STRINGS.verify, lang)}
        </Button>
        <Button size="small" variant="outlined" disabled={!enabled} onClick={(event) => setMenu(event.currentTarget)} data-testid={`restart-${id}`} aria-haspopup="menu">
          {pick(STRINGS.restartService, lang)}
        </Button>
        <Menu anchorEl={menu} open={menu !== null} onClose={() => setMenu(null)}>
          {SERVICES.map((service) => (
            <MenuItem
              key={service}
              data-testid={`restart-${id}-${service}`}
              onClick={() => {
                setMenu(null);
                onRestart(service);
              }}
            >
              {service}
            </MenuItem>
          ))}
        </Menu>
      </Stack>

      {line !== undefined ? (
        <Typography variant="body2" role="status" aria-live="polite" data-testid={`command-${id}`} data-status={line.status}>
          {line.text}
        </Typography>
      ) : null}

      {panel?.open ? (
        <Box data-testid={`models-panel-${id}`}>
          {panel.state === 'loading' && panel.models.length === 0 ? <Typography variant="body2">{pick(STRINGS.modelsLoading, lang)}</Typography> : null}
          {panel.state === 'error' ? <Typography variant="body2">{pick(STRINGS.modelsError, lang)}</Typography> : null}
          {panel.state === 'ready' && panel.models.length === 0 ? <Typography variant="body2">{pick(STRINGS.modelsNone, lang)}</Typography> : null}
          <Stack spacing={0.5}>
            {panel.models.map((model) => (
              <Stack key={model.id} direction="row" sx={{ alignItems: 'center', flexWrap: 'wrap', gap: 1 }}>
                <Typography variant="body2" sx={{ fontFamily: 'monospace', overflowWrap: 'anywhere' }}>
                  {model.id}
                </Typography>
                <Chip size="small" label={model.status} />
                {model.isDefault ? <Chip size="small" color="primary" label={pick(STRINGS.isDefault, lang)} /> : null}
                {model.status !== 'loaded' ? (
                  <Button
                    size="small"
                    disabled={!enabled || !model.loadable}
                    onClick={() => send({ type: 'model.load', params: { model_id: model.id, unload_others: true }, param: model.id })}
                    data-testid={`load-${id}-${model.id}`}
                  >
                    {pick(STRINGS.load, lang)}
                  </Button>
                ) : (
                  <>
                    <Button size="small" disabled={!enabled} onClick={() => send({ type: 'model.unload', params: { model_id: model.id }, param: model.id })} data-testid={`unload-${id}-${model.id}`}>
                      {pick(STRINGS.unload, lang)}
                    </Button>
                    {!model.isDefault ? (
                      <Button size="small" disabled={!enabled} onClick={() => send({ type: 'model.set_default', params: { model_id: model.id }, param: model.id })} data-testid={`default-${id}-${model.id}`}>
                        {pick(STRINGS.setDefault, lang)}
                      </Button>
                    ) : null}
                  </>
                )}
              </Stack>
            ))}
          </Stack>
        </Box>
      ) : null}
    </Stack>
  );
}
