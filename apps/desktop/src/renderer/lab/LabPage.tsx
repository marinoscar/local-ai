// The Lab tab (docs/specs/desktop.md 4.8): the same node cards as the cloud overview (`@localai/ui`), a banner when the cloud
// cannot be reached, and the everyday command buttons. All data comes through `window.localaiDesktop.cloud`; this page makes
// no network request of its own and never sees the token. What to fetch and when is the controller's (model.ts).
// Issue #515: the Workloads panel and "Make music on this node" (the studio over the cloud: text fields only).
import { Alert, Box, Button, Chip, Dialog, DialogActions, DialogContent, DialogContentText, DialogTitle, Menu, MenuItem, Stack, TextField, Typography } from '@mui/material';
import { NodeCard } from '@localai/ui';
import type { NodeCardDto } from '@localai/ui/lab/types';
import { type ReactElement, useEffect, useMemo, useState, useSyncExternalStore } from 'react';
import type { Lang } from '../../shared/types';
import { LabController, type CommandSpec, type LabBridge, type LabState } from './model';
import { MusicDialog } from './MusicDialog';
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
  const [musicNode, setMusicNode] = useState<string | null>(null);
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
          <NodeCard
            key={node.nodeId}
            node={node}
            actions={
              <NodeActions
                controller={controller}
                state={state}
                node={node}
                lang={lang}
                canAct={canAct}
                onRestart={(service) => setRestart({ nodeId: node.nodeId, service })}
                onMusic={() => {
                  setMusicNode(node.nodeId);
                  void controller.loadSongs(node.nodeId);
                }}
              />
            }
          />
        ))}
      </Box>

      <MusicDialog
        nodeId={musicNode}
        lang={lang}
        workloads={musicNode !== null ? state.workloads[musicNode] : undefined}
        canSend={musicNode !== null && canAct && !controller.isMusicBusy(musicNode)}
        onClose={() => setMusicNode(null)}
        onSend={(input) => {
          if (musicNode !== null) void controller.requestMusic(musicNode, input);
          setMusicNode(null);
        }}
      />

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
  onMusic(): void;
}

/** The footer of one card: Open in browser, the command buttons, the command's line and the models panel. */
function NodeActions({ controller, state, node, lang, canAct, onRestart, onMusic }: NodeActionsProps): ReactElement {
  const id = node.nodeId;
  const busy = controller.isBusy(id);
  const enabled = canAct && !busy && node.phase === 'enrolled';
  const line = state.commands[id];
  const panel = state.models[id];
  const workloads = state.workloads[id];
  const music = state.music[id];
  const songs = state.songs[id];
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
        <Button size="small" variant="outlined" disabled={!canAct || node.phase !== 'enrolled'} onClick={() => controller.toggleWorkloads(id)} data-testid={`workloads-${id}`}>
          {pick(workloads?.open ? STRINGS.hideWorkloads : STRINGS.workloads, lang)}
        </Button>
        <Button size="small" variant="outlined" disabled={!canAct || node.phase !== 'enrolled' || controller.isMusicBusy(id)} onClick={onMusic} data-testid={`music-${id}`}>
          {pick(STRINGS.makeMusic, lang)}
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

      {music !== undefined ? (
        <Typography variant="body2" role="status" aria-live="polite" data-testid={`music-line-${id}`} data-status={music.status}>
          {music.text}
        </Typography>
      ) : null}

      {workloads?.open ? (
        <Box data-testid={`workloads-panel-${id}`}>
          {workloads.state === 'loading' && workloads.workloads.length === 0 ? <Typography variant="body2">{pick(STRINGS.workloadsLoading, lang)}</Typography> : null}
          {workloads.state === 'error' ? <Typography variant="body2">{pick(STRINGS.workloadsError, lang)}</Typography> : null}
          {workloads.state === 'ready' && workloads.workloads.length === 0 ? <Typography variant="body2">{pick(STRINGS.workloadsNone, lang)}</Typography> : null}
          {workloads.workloads.length > 0 ? (
            <Typography variant="body2" color="text.secondary" sx={{ mb: 0.5 }}>
              {pick(STRINGS.workloadsHelp, lang)}
            </Typography>
          ) : null}
          <Stack direction="row" sx={{ flexWrap: 'wrap', gap: 1, alignItems: 'center' }}>
            {workloads.workloads.map((workload) => (
              <Stack key={workload.name} direction="row" sx={{ alignItems: 'center', gap: 0.5 }}>
                <Button
                  size="small"
                  variant={workload.active ? 'contained' : 'outlined'}
                  disabled={!enabled || workload.active}
                  aria-pressed={workload.active}
                  onClick={() => void controller.activateWorkload(id, workload.name)}
                  data-testid={`workload-${id}-${workload.name}`}
                >
                  {workload.label[lang]}
                </Button>
                {workload.active ? <Chip size="small" color="primary" label={pick(STRINGS.workloadActive, lang)} /> : null}
                {!workload.fits ? <Chip size="small" color="warning" label={pick(STRINGS.workloadDoesNotFit, lang)} /> : null}
                {workload.missing > 0 ? <Chip size="small" label={fill(pick(STRINGS.workloadMissing, lang), { count: String(workload.missing) })} /> : null}
              </Stack>
            ))}
          </Stack>
        </Box>
      ) : null}

      {songs !== undefined ? (
        <Box data-testid={`songs-${id}`}>
          <Typography variant="subtitle2">{pick(STRINGS.songs, lang)}</Typography>
          {songs.state === 'loading' && songs.songs.length === 0 ? <Typography variant="body2">{pick(STRINGS.songsLoading, lang)}</Typography> : null}
          {songs.state === 'error' ? <Typography variant="body2">{pick(STRINGS.songsError, lang)}</Typography> : null}
          {songs.state === 'ready' && songs.songs.length === 0 ? <Typography variant="body2">{pick(STRINGS.songsNone, lang)}</Typography> : null}
          {songs.songs.map((song) => (
            <Typography key={song.id} variant="body2" data-testid={`song-${song.id}`} sx={{ overflowWrap: 'anywhere' }}>
              {song.title !== '' ? song.title : pick(STRINGS.untitled, lang)}
              {song.durationS !== null ? ` · ${Math.floor(song.durationS / 60)}:${String(song.durationS % 60).padStart(2, '0')}` : ''}
            </Typography>
          ))}
          {songs.songs.length > 0 ? (
            <Typography variant="caption" color="text.secondary">
              {pick(STRINGS.songsListen, lang)}
            </Typography>
          ) : null}
        </Box>
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
