// The person's songs in progress (docs/specs/cloud-app.md section 3.11, #514): waiting on the node -> making the song
// -> done, or how it ended with the doctor code and fix. A waiting request can be cancelled. Each is read every few
// seconds while it is active and the tab is visible; a settled one stays until dismissed.
import { Alert, Box, Button, LinearProgress, Typography } from '@mui/material';
import { useEffect, useRef, useState } from 'react';

import { useT } from '../../i18n/LanguageProvider';
import { cancelMusicRequest, getMusicRequest, listActiveRequests, type MusicRequest } from './musicApi';
import { refusalText, requestErrorText, stageText } from './musicText';

export interface RequestProgressProps {
  /** Requests made on this page (the newest first); the list also adopts the person's active ones on load. */
  created: MusicRequest[];
  /** Called once per request that finished with songs (the library reads again). */
  onDone: (request: MusicRequest) => void;
  pollMs?: number;
}

const ACTIVE = new Set(['queued', 'generating']);

export function RequestProgress({ created, onDone, pollMs = 3000 }: RequestProgressProps) {
  const t = useT();
  const [requests, setRequests] = useState<MusicRequest[]>([]);
  const [problem, setProblem] = useState<string | null>(null);
  const done = useRef(new Set<string>());
  const requestsRef = useRef(requests);
  requestsRef.current = requests;
  const onDoneRef = useRef(onDone);
  onDoneRef.current = onDone;

  const upsert = (request: MusicRequest) => {
    setRequests((current) => [request, ...current.filter((item) => item.id !== request.id)].sort((a, b) => b.createdAt.localeCompare(a.createdAt)));
    if (request.stage === 'done' && !done.current.has(request.id)) {
      done.current.add(request.id);
      onDoneRef.current(request);
    }
  };

  useEffect(() => {
    listActiveRequests()
      .then((page) => page.items.forEach(upsert))
      .catch(() => undefined);
  }, []);

  useEffect(() => {
    created.forEach(upsert);
  }, [created]);

  const hasActive = requests.some((request) => ACTIVE.has(request.stage));
  useEffect(() => {
    if (!hasActive) return undefined;
    const timer = setInterval(() => {
      if (document.visibilityState === 'hidden') return;
      for (const request of requestsRef.current.filter((item) => ACTIVE.has(item.stage))) {
        getMusicRequest(request.id).then(upsert).catch(() => undefined);
      }
    }, pollMs);
    return () => clearInterval(timer);
  }, [hasActive, pollMs]);

  const cancel = async (id: string) => {
    setProblem(null);
    try {
      upsert(await cancelMusicRequest(id));
    } catch (error) {
      setProblem(refusalText(error, t));
    }
  };

  if (requests.length === 0) return null;
  return (
    <Box component="section" aria-labelledby="music-progress-title" sx={{ display: 'flex', flexDirection: 'column', gap: 1 }}>
      <Typography id="music-progress-title" variant="h6" component="h2">
        {t('In progress', 'En curso')}
      </Typography>
      {problem ? (
        <Alert severity="error" role="alert" onClose={() => setProblem(null)}>
          {problem}
        </Alert>
      ) : null}
      <Box component="ul" aria-live="polite" sx={{ m: 0, p: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 1 }}>
        {requests.map((request) => {
          const error = requestErrorText(request.error);
          return (
            <Box component="li" key={request.id} data-testid={`request-${request.id}`} sx={{ border: 1, borderColor: 'divider', borderRadius: 1, p: 1.5, minWidth: 0 }}>
              <Typography sx={{ fontWeight: 600, overflowWrap: 'anywhere' }}>{request.title}</Typography>
              <Typography color="text.secondary" data-testid="request-stage">
                {stageText(request, t)}
              </Typography>
              {ACTIVE.has(request.stage) ? <LinearProgress sx={{ my: 1 }} aria-label={stageText(request, t)} /> : null}
              {error ? (
                <Alert severity="error" sx={{ mt: 1 }}>
                  {error}
                </Alert>
              ) : null}
              <Box sx={{ display: 'flex', gap: 1, mt: 1 }}>
                {request.stage === 'queued' ? (
                  <Button size="small" variant="outlined" onClick={() => void cancel(request.id)}>
                    {t('Cancel', 'Cancelar')}
                  </Button>
                ) : null}
                {!ACTIVE.has(request.stage) ? (
                  <Button size="small" onClick={() => setRequests((current) => current.filter((item) => item.id !== request.id))}>
                    {t('Dismiss', 'Ocultar')}
                  </Button>
                ) : null}
              </Box>
            </Box>
          );
        })}
      </Box>
    </Box>
  );
}
