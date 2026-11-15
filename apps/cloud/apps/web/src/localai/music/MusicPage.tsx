// The music page (`/music`, localai_music:use; docs/specs/cloud-app.md section 3.11, #514, #568): the first feature
// page outside `/lab`. State first: which computer can make music now and the one next thing to do; then My prompts
// (the saved history: open one to edit, make again or delete it; or start a new one), the song form, the songs in
// progress and the library across nodes. It never talks to a node: every action is a request to the cloud, which
// queues a command the node pulls. Works at phone width (one column, nothing wider than the screen).
import { Alert, Box, Button, Typography } from '@mui/material';
import { usePermissions } from '@marinoscar/platform-web/identity/headless';
import { useCallback, useEffect, useState } from 'react';

import { useT } from '../../i18n/LanguageProvider';
import { usePolledResource } from '../usePolledResource';
import { MusicForm } from './MusicForm';
import { NodePicker } from './NodePicker';
import { PromptHistory } from './PromptHistory';
import { RequestProgress } from './RequestProgress';
import { SongLibrary } from './SongLibrary';
import { getAssist, getPrompt, listMusicNodes, listPrompts, listSongs, type MusicAssist, type MusicPrompt, type MusicRequest } from './musicApi';
import { refusalText } from './musicText';

export const MUSIC_PERMISSION = 'localai_music:use';

export interface MusicPageProps {
  /** Poll intervals (tests shorten them). */
  nodesPollMs?: number;
  libraryPollMs?: number;
  requestPollMs?: number;
  commandPollMs?: number;
}

export function MusicPage({ nodesPollMs = 5000, libraryPollMs = 15000, requestPollMs = 3000, commandPollMs = 2000 }: MusicPageProps) {
  const t = useT();
  const { hasPermission } = usePermissions();
  const nodes = usePolledResource(listMusicNodes, nodesPollMs);
  const library = usePolledResource(() => listSongs(), libraryPollMs);
  const prompts = usePolledResource(() => listPrompts(), libraryPollMs * 4);
  const [selected, setSelected] = useState<string | null>(null);
  const [created, setCreated] = useState<MusicRequest[]>([]);
  const [prompt, setPrompt] = useState<MusicPrompt | null>(null);
  const [blankForm, setBlankForm] = useState(0);
  const [assist, setAssist] = useState<MusicAssist | null>(null);
  const [openProblem, setOpenProblem] = useState<string | null>(null);

  // Choose the first ready computer once the list arrives (else the first one).
  useEffect(() => {
    if (selected !== null || !nodes.data || nodes.data.length === 0) return;
    setSelected((nodes.data.find((node) => node.readiness === 'ready') ?? nodes.data[0]).nodeId);
  }, [nodes.data, selected]);

  // Whether the AI helpers work (once; an error reads as "off", and the form says why).
  useEffect(() => {
    let alive = true;
    getAssist()
      .then((value) => alive && setAssist(value))
      .catch(() => alive && setAssist({ ai: { enabled: false, reason: 'ai_off', fix: 'Turn on AI in Settings → AI.' } }));
    return () => {
      alive = false;
    };
  }, []);

  const node = nodes.data?.find((candidate) => candidate.nodeId === selected) ?? null;
  const refreshLibrary = library.refresh;
  const refreshPrompts = prompts.refresh;

  const open = useCallback(
    async (id: string) => {
      setOpenProblem(null);
      try {
        setPrompt(await getPrompt(id));
      } catch (error) {
        setOpenProblem(refusalText(error, t));
      }
    },
    [t],
  );

  const onCreated = useCallback(
    (request: MusicRequest) => {
      setCreated((current) => [request, ...current]);
      refreshPrompts();
      // The song saved (or updated) its prompt: keep it open, so the next Save or Make goes to the same prompt.
      if (request.promptId) void open(request.promptId);
    },
    [open, refreshPrompts],
  );

  const onSaved = useCallback(
    (saved: MusicPrompt) => {
      setPrompt(saved);
      refreshPrompts();
    },
    [refreshPrompts],
  );

  const onDeleted = useCallback(() => {
    setPrompt(null);
    setBlankForm((count) => count + 1);
    refreshPrompts();
    refreshLibrary();
  }, [refreshLibrary, refreshPrompts]);

  const newPrompt = () => {
    setPrompt(null);
    setBlankForm((count) => count + 1);
  };

  return (
    <Box component="section" aria-labelledby="music-title" sx={{ display: 'flex', flexDirection: 'column', gap: 3, maxWidth: 900, minWidth: 0 }}>
      <Typography id="music-title" variant="h4" component="h1">
        {t('Music', 'Música')}
      </Typography>
      {nodes.error && !nodes.data ? (
        <Alert severity="error" action={<Button color="inherit" size="small" onClick={nodes.refresh}>{t('Try again', 'Intentar otra vez')}</Button>}>
          {t('Could not load the computers.', 'No se pudieron cargar las computadoras.')}
        </Alert>
      ) : null}
      {nodes.data ? (
        <NodePicker
          nodes={nodes.data}
          selected={selected}
          onSelect={setSelected}
          canSwitch={hasPermission('localai_workloads:write')}
          onSwitched={nodes.refresh}
          commandPollMs={commandPollMs}
        />
      ) : null}
      <PromptHistory
        page={prompts.data}
        loading={prompts.loading}
        failed={prompts.error !== null}
        openId={prompt?.id ?? null}
        onOpen={(id) => void open(id)}
        onNew={newPrompt}
        onRetry={refreshPrompts}
      />
      {openProblem ? (
        <Alert severity="error" role="alert" onClose={() => setOpenProblem(null)}>
          {openProblem}
        </Alert>
      ) : null}
      <MusicForm
        key={prompt ? `prompt-${prompt.id}` : `new-${blankForm}`}
        nodeId={node?.nodeId ?? null}
        ready={node?.readiness === 'ready'}
        autoDuration={node?.autoDuration ?? true}
        prompt={prompt}
        assist={assist}
        onCreated={onCreated}
        onSaved={onSaved}
        onDeleted={onDeleted}
      />
      <RequestProgress created={created} onDone={refreshLibrary} pollMs={requestPollMs} />
      <SongLibrary page={library.data} loading={library.loading} canDelete={hasPermission('localai_music:delete')} onChanged={refreshLibrary} />
    </Box>
  );
}

export default MusicPage;
