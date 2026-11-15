// "My prompts" (#568, docs/specs/cloud-app.md section 3.11): the person's saved prompts, newest used first, each with
// its title, description, when it was last used and how many songs it made. "Open" loads one into the form; "New
// prompt" empties the form. One column, nothing wider than a phone's screen.
import { Alert, Box, Button, Typography } from '@mui/material';

import { useT } from '../../i18n/LanguageProvider';
import type { MusicPromptSummary, Page } from './musicApi';

export interface PromptHistoryProps {
  page: Page<MusicPromptSummary> | null;
  loading: boolean;
  failed: boolean;
  /** The prompt in the form, shown as open. */
  openId: string | null;
  onOpen: (id: string) => void;
  onNew: () => void;
  onRetry: () => void;
}

export function PromptHistory({ page, loading, failed, openId, onOpen, onNew, onRetry }: PromptHistoryProps) {
  const t = useT();
  return (
    <Box component="section" aria-labelledby="music-prompts-title" sx={{ display: 'flex', flexDirection: 'column', gap: 1, minWidth: 0 }}>
      <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 1, alignItems: 'center', justifyContent: 'space-between' }}>
        <Typography id="music-prompts-title" variant="h6" component="h2">
          {t('My prompts', 'Mis indicaciones')}
        </Typography>
        <Button variant="outlined" onClick={onNew}>
          {t('New prompt', 'Nueva indicación')}
        </Button>
      </Box>
      {failed && !page ? (
        <Alert severity="error" action={<Button color="inherit" size="small" onClick={onRetry}>{t('Try again', 'Intentar otra vez')}</Button>}>
          {t('Could not load your prompts.', 'No se pudieron cargar sus indicaciones.')}
        </Alert>
      ) : null}
      {!page && loading ? <Typography color="text.secondary">{t('Loading…', 'Cargando…')}</Typography> : null}
      {page && page.items.length === 0 ? (
        <Typography color="text.secondary">{t('No saved prompts yet. Each song you make is saved here.', 'Todavía no hay indicaciones. Cada canción que crea se guarda aquí.')}</Typography>
      ) : null}
      <Box component="ul" sx={{ m: 0, p: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 1 }}>
        {(page?.items ?? []).map((prompt) => (
          <Box
            component="li"
            key={prompt.id}
            data-testid={`prompt-${prompt.id}`}
            sx={{ border: 1, borderColor: prompt.id === openId ? 'primary.main' : 'divider', borderRadius: 1, p: 1.5, minWidth: 0 }}
          >
            <Typography sx={{ fontWeight: 600, overflowWrap: 'anywhere' }}>{prompt.title}</Typography>
            <Typography variant="body2" color="text.secondary" sx={{ overflowWrap: 'anywhere', display: '-webkit-box', WebkitLineClamp: 2, WebkitBoxOrient: 'vertical', overflow: 'hidden' }}>
              {prompt.description}
            </Typography>
            <Typography variant="body2" color="text.secondary">
              {[
                `${t('Last used', 'Último uso')} ${new Date(prompt.lastUsedAt).toLocaleDateString()}`,
                prompt.songCount === 1 ? t('1 song', '1 canción') : t(`${prompt.songCount} songs`, `${prompt.songCount} canciones`),
              ].join(' · ')}
            </Typography>
            <Box sx={{ mt: 1 }}>
              <Button size="small" variant={prompt.id === openId ? 'contained' : 'outlined'} onClick={() => onOpen(prompt.id)} aria-label={`${prompt.id === openId ? t('Open now', 'Abierta') : t('Open', 'Abrir')} ${prompt.title}`}>
                {prompt.id === openId ? t('Open now', 'Abierta') : t('Open', 'Abrir')}
              </Button>
            </Box>
          </Box>
        ))}
      </Box>
    </Box>
  );
}
