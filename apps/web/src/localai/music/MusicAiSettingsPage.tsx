// Administration → AI → "Use AI for music" (`/admin/settings/ai/music`, #570, docs/specs/cloud-app.md section 3.10):
// the one switch that lets the music page use the cloud's AI to write lyrics and titles. Default OFF. Reading needs
// `ai_config:read` and changing it `ai_config:write`, the permissions `GET`/`PUT /api/localai/music/ai-settings` enforce
// (the AI slice's administrator permissions). Like every AI card but the AI switch itself, the card is hidden while AI
// is off (`feature: 'ai'`).
//
// State first: is music AI on? Then the one switch, and what to set up first (a provider, a key and a model in
// Settings → AI).
import { Alert, Box, CircularProgress, FormControlLabel, Link, Paper, Stack, Switch, Typography } from '@mui/material';
import { usePermissions } from '@marinoscar/platform-web/identity/headless';
import { useCallback, useEffect, useState } from 'react';
import { Link as RouterLink } from 'react-router-dom';

import { useT } from '../../i18n/LanguageProvider';
import { getMusicAiSettings as defaultGet, putMusicAiSettings as defaultPut, type MusicAiSettings } from './musicApi';
import { refusalText } from './musicText';

export interface MusicAiSettingsPageProps {
  /** Replace the transports (tests). */
  getSettings?: () => Promise<MusicAiSettings>;
  putSettings?: (value: MusicAiSettings) => Promise<MusicAiSettings>;
}

export function MusicAiSettingsPage({ getSettings = defaultGet, putSettings = defaultPut }: MusicAiSettingsPageProps) {
  const t = useT();
  const { hasPermission } = usePermissions();
  const canWrite = hasPermission('ai_config:write');
  const [value, setValue] = useState<MusicAiSettings | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setValue(await getSettings());
      setLoadError(null);
    } catch (error) {
      setLoadError(refusalText(error, t));
    }
  }, [getSettings, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const change = async (aiEnabled: boolean) => {
    setBusy(true);
    setProblem(null);
    try {
      setValue(await putSettings({ aiEnabled }));
    } catch (error) {
      setProblem(refusalText(error, t));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2, maxWidth: 720 }}>
      <Typography variant="h4" component="h1">
        {t('Use AI for music', 'Usar IA para música')}
      </Typography>
      {loadError ? (
        <Alert severity="error" role="alert">
          {loadError}
        </Alert>
      ) : value === null ? (
        <CircularProgress aria-label={t('Loading', 'Cargando')} />
      ) : (
        <>
          <Alert severity={value.aiEnabled ? 'success' : 'info'} role="status" data-testid="music-ai-state">
            {value.aiEnabled
              ? t(
                  'Music AI is on. On the Music page, the AI writes lyrics and suggests titles for people who have an AI model.',
                  'La IA para música está encendida. En la página Música, la IA escribe letras y sugiere títulos para quien tenga un modelo de IA.',
                )
              : t(
                  'Music AI is off. On the Music page, empty lyrics make a song with no vocals, and titles are made from the words.',
                  'La IA para música está apagada. En la página Música, sin letra la canción no tiene voz, y los títulos se hacen con las palabras.',
                )}
          </Alert>
          <Paper variant="outlined" sx={{ p: 2 }}>
            <Stack spacing={1}>
              <FormControlLabel
                control={<Switch checked={value.aiEnabled} disabled={!canWrite || busy} onChange={(event) => void change(event.target.checked)} />}
                label={t('Use AI for music', 'Usar IA para música')}
              />
              <Typography variant="body2" color="text.secondary">
                {t(
                  'When it is on, "Write lyrics now" and "Suggest a title" use the AI, and a song in Automatic mode with no lyrics gets lyrics written by the AI when Make the song is pressed. The lyrics and the description are sent to the AI model you set up; they are never written to a log.',
                  'Cuando está encendido, "Escribir la letra ahora" y "Sugerir un título" usan la IA, y una canción en modo Automático sin letra recibe una letra escrita por la IA al pulsar Crear la canción. La letra y la descripción se envían al modelo de IA que usted configuró; nunca se escriben en un registro.',
                )}
              </Typography>
              {!canWrite ? (
                <Typography variant="body2" color="text.secondary" data-testid="music-ai-read-only">
                  {t('Only an AI administrator can change this.', 'Solo un administrador de IA puede cambiar esto.')}
                </Typography>
              ) : null}
            </Stack>
          </Paper>
          <Typography variant="body2">
            {t('First set up a provider: ', 'Primero configure un proveedor: ')}
            <Link component={RouterLink} to="/admin/settings/ai">
              {t('Settings → AI', 'Ajustes (Settings) → AI')}
            </Link>
            {t(' → add a provider and its key → pick a model in AI Models.', ' → agregue un proveedor y su clave → elija un modelo en AI Models.')}
          </Typography>
          {problem ? (
            <Alert severity="error" role="alert">
              {problem}
            </Alert>
          ) : null}
        </>
      )}
    </Box>
  );
}

export default MusicAiSettingsPage;
