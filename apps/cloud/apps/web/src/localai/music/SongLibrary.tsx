// The song library across every node (docs/specs/cloud-app.md section 3.11, #514): newest first, play in the browser,
// download, and delete the cloud copy (asks first; needs `localai_music:delete`). The browser's `<audio>` cannot send
// the Authorization header, so Play and Download ask the cloud for a 5-minute signed URL first.
//
// LOCAL STAND-IN: `packages/ui` is getting a shared `SongLibrary` (#512) in parallel. Swap this one for it (with a
// cloud transport) once it lands.
import { Alert, Box, Button, Typography } from '@mui/material';
import { useState } from 'react';

import { useT } from '../../i18n/LanguageProvider';
import { ConfirmDialog } from '../node/ConfirmDialog';
import { deleteSong, songAudioUrl, type LibrarySong, type Page } from './musicApi';
import { formatDuration, refusalText } from './musicText';

export interface SongLibraryProps {
  page: Page<LibrarySong> | null;
  loading: boolean;
  canDelete: boolean;
  onChanged: () => void;
}

export function SongLibrary({ page, loading, canDelete, onChanged }: SongLibraryProps) {
  const t = useT();
  const [playing, setPlaying] = useState<{ id: string; url: string } | null>(null);
  const [toDelete, setToDelete] = useState<LibrarySong | null>(null);
  const [problem, setProblem] = useState<string | null>(null);

  const play = async (song: LibrarySong) => {
    setProblem(null);
    try {
      setPlaying({ id: song.id, url: (await songAudioUrl(song.id)).url });
    } catch (error) {
      setProblem(refusalText(error, t));
    }
  };

  const download = async (song: LibrarySong) => {
    setProblem(null);
    try {
      const { url } = await songAudioUrl(song.id, true);
      const link = document.createElement('a');
      link.href = url;
      link.rel = 'noopener';
      document.body.appendChild(link);
      link.click();
      link.remove();
    } catch (error) {
      setProblem(refusalText(error, t));
    }
  };

  return (
    <Box component="section" aria-labelledby="music-library-title" sx={{ display: 'flex', flexDirection: 'column', gap: 1 }}>
      <Typography id="music-library-title" variant="h6" component="h2">
        {t('Songs', 'Canciones')}
      </Typography>
      {problem ? (
        <Alert severity="error" role="alert" onClose={() => setProblem(null)}>
          {problem}
        </Alert>
      ) : null}
      {!page && loading ? <Typography color="text.secondary">{t('Loading…', 'Cargando…')}</Typography> : null}
      {page && page.items.length === 0 ? <Typography color="text.secondary">{t('No songs yet.', 'Todavía no hay canciones.')}</Typography> : null}
      <Box component="ul" sx={{ m: 0, p: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 1 }}>
        {(page?.items ?? []).map((song) => (
          <Box component="li" key={song.id} data-testid={`song-${song.id}`} sx={{ border: 1, borderColor: 'divider', borderRadius: 1, p: 1.5, minWidth: 0 }}>
            <Typography sx={{ fontWeight: 600, overflowWrap: 'anywhere' }}>
              {song.title}
              {song.index > 0 ? ` (${song.index + 1})` : ''}
            </Typography>
            <Typography variant="body2" color="text.secondary" sx={{ overflowWrap: 'anywhere' }}>
              {[song.nodeName, formatDuration(song.durationS), new Date(song.createdAt).toLocaleDateString()].filter(Boolean).join(' · ')}
            </Typography>
            {playing?.id === song.id ? (
              <Box component="audio" controls autoPlay src={playing.url} sx={{ width: '100%', mt: 1 }} aria-label={`${t('Play', 'Reproducir')} ${song.title}`} />
            ) : null}
            <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 1, mt: 1 }}>
              <Button size="small" variant="outlined" onClick={() => void play(song)} aria-label={`${t('Play', 'Reproducir')} ${song.title}`}>
                {t('Play', 'Reproducir')}
              </Button>
              <Button size="small" onClick={() => void download(song)} aria-label={`${t('Download', 'Descargar')} ${song.title}`}>
                {t('Download', 'Descargar')}
              </Button>
              {canDelete ? (
                <Button size="small" color="error" onClick={() => setToDelete(song)} aria-label={`${t('Delete', 'Eliminar')} ${song.title}`}>
                  {t('Delete', 'Eliminar')}
                </Button>
              ) : null}
            </Box>
          </Box>
        ))}
      </Box>
      <ConfirmDialog
        open={toDelete !== null}
        title={t('Delete this song?', '¿Eliminar esta canción?')}
        confirmLabel={t('Delete', 'Eliminar')}
        destructive
        onClose={() => setToDelete(null)}
        onConfirm={async () => {
          if (!toDelete) return null;
          try {
            await deleteSong(toDelete.id);
            if (playing?.id === toDelete.id) setPlaying(null);
            setToDelete(null);
            onChanged();
            return null;
          } catch (error) {
            return refusalText(error, t);
          }
        }}
      >
        {toDelete
          ? t(
              `"${toDelete.title}" is removed from the cloud for everyone. The copy on ${toDelete.nodeName} stays there.`,
              `"${toDelete.title}" se elimina de la nube para todos. La copia en ${toDelete.nodeName} se queda allí.`,
            )
          : null}
      </ConfirmDialog>
    </Box>
  );
}
