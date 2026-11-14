// "Make music on this node" (issue #515): the studio over the cloud, as text fields. The request goes through the Lab's
// allowlisted proxy (`POST /api/localai/music/requests`); the node makes the song and the cloud keeps it. No sample file
// here: a sample goes through the Music window on the computer itself, or the cloud's own Music page.
import { Alert, Button, Dialog, DialogActions, DialogContent, DialogContentText, DialogTitle, MenuItem, Stack, TextField } from '@mui/material';
import { type ReactElement, useEffect, useState } from 'react';
import type { Lang } from '../../shared/types';
import { MUSIC_FORM_DEFAULTS, type MusicForm, type MusicInput, type WorkloadsPanel, musicInputFrom, musicWorkloadInactive } from './model';
import { STRINGS, fill, pick } from './strings';

export interface MusicDialogProps {
  /** The node the dialog is for; null: closed. */
  nodeId: string | null;
  lang: Lang;
  /** The node's workloads, when the Workloads panel read them (for the "switch to Music first" hint). */
  workloads: WorkloadsPanel | undefined;
  canSend: boolean;
  onClose(): void;
  onSend(input: MusicInput): void;
}

export function MusicDialog({ nodeId, lang, workloads, canSend, onClose, onSend }: MusicDialogProps): ReactElement {
  const [form, setForm] = useState<MusicForm>(MUSIC_FORM_DEFAULTS);
  const [problem, setProblem] = useState<string | null>(null);
  // A new node starts from the defaults (the text of the last one is not carried to another computer).
  useEffect(() => {
    setForm({ ...MUSIC_FORM_DEFAULTS, language: lang });
    setProblem(null);
  }, [nodeId, lang]);

  const node = nodeId ?? '';
  const set = (patch: Partial<MusicForm>): void => {
    setForm((current) => ({ ...current, ...patch }));
    setProblem(null);
  };
  const submit = (): void => {
    const checked = musicInputFrom(form);
    if (!checked.ok) {
      setProblem(pick(STRINGS[checked.problem], lang));
      return;
    }
    onSend(checked.input);
  };

  return (
    <Dialog open={nodeId !== null} onClose={onClose} aria-labelledby="music-title" fullWidth maxWidth="sm">
      <DialogTitle id="music-title">{fill(pick(STRINGS.musicTitle, lang), { node })}</DialogTitle>
      <DialogContent>
        <Stack spacing={2} sx={{ pt: 1 }}>
          <DialogContentText>{fill(pick(STRINGS.musicIntro, lang), { node })}</DialogContentText>
          {musicWorkloadInactive(workloads) ? (
            <Alert severity="info" data-testid="music-not-active">
              {pick(STRINGS.musicNotActive, lang)}
            </Alert>
          ) : null}
          <TextField label={pick(STRINGS.lyrics, lang)} value={form.lyrics} onChange={(event) => set({ lyrics: event.target.value })} multiline minRows={5} maxRows={12} autoFocus slotProps={{ htmlInput: { 'data-testid': 'music-lyrics' } }} />
          <TextField label={pick(STRINGS.tags, lang)} value={form.tags} onChange={(event) => set({ tags: event.target.value })} slotProps={{ htmlInput: { maxLength: 512, 'data-testid': 'music-tags' } }} />
          <Stack direction="row" spacing={2}>
            <TextField
              label={pick(STRINGS.duration, lang)}
              type="number"
              value={form.duration}
              onChange={(event) => set({ duration: event.target.value })}
              slotProps={{ htmlInput: { min: 10, max: 240, step: 5, 'data-testid': 'music-duration' } }}
              sx={{ flex: 1 }}
            />
            <TextField
              select
              label={pick(STRINGS.variations, lang)}
              value={form.variations}
              onChange={(event) => set({ variations: event.target.value })}
              sx={{ flex: 1 }}
              slotProps={{ htmlInput: { 'data-testid': 'music-variations' } }}
            >
              {['1', '2', '3', '4'].map((count) => (
                <MenuItem key={count} value={count}>
                  {count}
                </MenuItem>
              ))}
            </TextField>
          </Stack>
          <TextField select label={pick(STRINGS.language, lang)} value={form.language} onChange={(event) => set({ language: event.target.value === 'es' ? 'es' : 'en' })} slotProps={{ htmlInput: { 'data-testid': 'music-language' } }}>
            <MenuItem value="en">{pick(STRINGS.english, lang)}</MenuItem>
            <MenuItem value="es">{pick(STRINGS.spanish, lang)}</MenuItem>
          </TextField>
          {problem !== null ? (
            <Alert severity="warning" role="alert" data-testid="music-problem">
              {problem}
            </Alert>
          ) : null}
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>{pick(STRINGS.cancel, lang)}</Button>
        <Button variant="contained" disabled={!canSend} onClick={submit} data-testid="music-send">
          {pick(STRINGS.makeIt, lang)}
        </Button>
      </DialogActions>
    </Dialog>
  );
}
