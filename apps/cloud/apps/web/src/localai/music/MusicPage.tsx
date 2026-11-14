// The music page (`/music`, localai_music:use; docs/specs/cloud-app.md section 3.11, #514): the first feature page
// outside `/lab`. State first: which computer can make music now and the one next thing to do; then the song form,
// the songs in progress and the library across nodes. It never talks to a node: every action is a request to the
// cloud, which queues a command the node pulls. Works at phone width (one column, nothing wider than the screen).
import { Alert, Box, Button, Typography } from '@mui/material';
import { usePermissions } from '@marinoscar/platform-web/identity/headless';
import { useCallback, useEffect, useState } from 'react';

import { useT } from '../../i18n/LanguageProvider';
import { usePolledResource } from '../usePolledResource';
import { MusicForm } from './MusicForm';
import { NodePicker } from './NodePicker';
import { RequestProgress } from './RequestProgress';
import { SongLibrary } from './SongLibrary';
import { listMusicNodes, listSongs, type MusicRequest } from './musicApi';

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
  const [selected, setSelected] = useState<string | null>(null);
  const [created, setCreated] = useState<MusicRequest[]>([]);

  // Choose the first ready computer once the list arrives (else the first one).
  useEffect(() => {
    if (selected !== null || !nodes.data || nodes.data.length === 0) return;
    setSelected((nodes.data.find((node) => node.readiness === 'ready') ?? nodes.data[0]).nodeId);
  }, [nodes.data, selected]);

  const node = nodes.data?.find((candidate) => candidate.nodeId === selected) ?? null;
  const onCreated = useCallback((request: MusicRequest) => setCreated((current) => [request, ...current]), []);
  const refreshLibrary = library.refresh;

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
      <MusicForm nodeId={node?.nodeId ?? null} ready={node?.readiness === 'ready'} onCreated={onCreated} />
      <RequestProgress created={created} onDone={refreshLibrary} pollMs={requestPollMs} />
      <SongLibrary page={library.data} loading={library.loading} canDelete={hasPermission('localai_music:delete')} onChanged={refreshLibrary} />
    </Box>
  );
}

export default MusicPage;
