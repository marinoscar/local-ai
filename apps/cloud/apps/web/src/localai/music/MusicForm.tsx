// The "make a song" form (docs/specs/cloud-app.md section 3.11, music.md section 3.7, #514, #568, #570): a Title with
// "Suggest a title", "Describe your song" (required), how the song gets its words (Automatic, the default / Use my
// lyrics / Write lyrics now / No vocals), the lyrics (optional in Automatic: a one-line hint says whether the AI will
// write them at Make the song or the song will have no vocals), an optional sample with "Sounds like this" / "Same song, my lyrics", and More options
// (length, language, versions, seed, strength). The limits are the API's (description 500 characters and 512 bytes,
// lyrics 8 KiB, title 80 characters, sample 20 MB), checked here first so nothing is uploaded in vain; the API checks
// again.
//
// A loaded prompt (from My prompts) fills the form. Then: Save (changes it), Save as new (a copy, with its saved
// sample), Make the song (makes it and saves what was sent into the prompt) and Delete (asks first; its songs stay).
// Without a loaded prompt, Save keeps a new one and Make the song saves one automatically.
//
// LOCAL STAND-IN: `packages/ui` is getting a shared `MusicStudio` (#512) in parallel. Swap this form for it (with a
// cloud transport: `createMusicRequest`) once it lands; the fields here are the contract's.
import {
  Accordion,
  AccordionDetails,
  AccordionSummary,
  Alert,
  Box,
  Button,
  FormControl,
  FormControlLabel,
  FormHelperText,
  FormLabel,
  MenuItem,
  Radio,
  RadioGroup,
  TextField,
  Typography,
} from '@mui/material';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import { useId, useState, type FormEvent } from 'react';

import { useLanguage, useT } from '../../i18n/LanguageProvider';
import { ConfirmDialog } from '../node/ConfirmDialog';
import {
  DESCRIPTION_MAX_CHARS,
  LYRICS_MAX_BYTES,
  SAMPLE_MAX_BYTES,
  TAGS_MAX_BYTES,
  TITLE_MAX_CHARS,
  charCount,
  createMusicRequest,
  createPrompt,
  deletePrompt,
  suggestLyrics,
  suggestTitle,
  updatePrompt,
  utf8Bytes,
  type LyricsMode,
  type MusicAssist,
  type MusicPrompt,
  type MusicPromptInput,
  type MusicRequest,
  type SampleMode,
} from './musicApi';
import { autoLyricsHint, formatBytes, musicAiOffText, refusalText } from './musicText';

export interface MusicFormProps {
  nodeId: string | null;
  /** The chosen node can make music now (its readiness is `ready`). */
  ready: boolean;
  /** The saved prompt loaded into the form, or null for a new one. The page remounts the form when it changes. */
  prompt: MusicPrompt | null;
  /** Whether the AI helpers work now (null while unknown: treated as off). */
  assist: MusicAssist | null;
  onCreated: (request: MusicRequest) => void;
  /** A prompt was saved (Save, Save as new): the page loads it and refreshes the history. */
  onSaved: (prompt: MusicPrompt) => void;
  /** The loaded prompt was deleted. */
  onDeleted: (id: string) => void;
}

const SAMPLE_TYPES = 'audio/wav,audio/x-wav,audio/mpeg,audio/flac,audio/ogg,.wav,.mp3,.flac,.ogg';

export function MusicForm({ nodeId, ready, prompt, assist, onCreated, onSaved, onDeleted }: MusicFormProps) {
  const t = useT();
  const { lang } = useLanguage();
  const sampleId = useId();
  const [title, setTitle] = useState(prompt?.title ?? '');
  const [titleNote, setTitleNote] = useState<string | null>(null);
  const [description, setDescription] = useState(prompt?.description ?? '');
  const [lyricsMode, setLyricsMode] = useState<LyricsMode>(prompt?.lyricsMode ?? 'auto');
  const [lyrics, setLyrics] = useState(prompt?.lyrics ?? '');
  const [sample, setSample] = useState<File | null>(null);
  const [removeSaved, setRemoveSaved] = useState(false);
  const [mode, setMode] = useState<SampleMode>(prompt?.sampleMode ?? 'sounds_like');
  const [duration, setDuration] = useState(String(prompt?.durationS ?? 60));
  const [language, setLanguage] = useState(prompt?.language ?? 'en');
  const [variations, setVariations] = useState(String(prompt?.variations ?? 1));
  const [seed, setSeed] = useState(prompt?.seed !== null && prompt?.seed !== undefined ? String(prompt.seed) : '');
  const [strength, setStrength] = useState(prompt?.strength !== null && prompt?.strength !== undefined ? String(prompt.strength) : '');
  const [busy, setBusy] = useState<null | 'make' | 'save' | 'copy' | 'title' | 'lyrics'>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);

  const savedSample = prompt?.hasSample === true && !removeSaved;
  const hasSample = sample !== null || savedSample;
  const aiOn = assist?.ai.enabled === true;
  const lyricsBytes = utf8Bytes(lyrics);
  const descriptionChars = charCount(description);
  const durationN = Number(duration);
  const seedN = seed.trim() === '' ? undefined : Number(seed);
  const strengthN = strength.trim() === '' ? undefined : Number(strength);
  const needsLyrics = lyricsMode !== 'instrumental';
  const issues = [
    charCount(title.trim()) > TITLE_MAX_CHARS ? t('The title is too long (80 characters at most).', 'El título es demasiado largo (80 caracteres como máximo).') : null,
    descriptionChars > DESCRIPTION_MAX_CHARS ? t('The description is too long (500 characters at most).', 'La descripción es demasiado larga (500 caracteres como máximo).') : null,
    descriptionChars <= DESCRIPTION_MAX_CHARS && utf8Bytes(description) > TAGS_MAX_BYTES
      ? t('The description is too long for the computer: use fewer accented letters or symbols.', 'La descripción es demasiado larga para la computadora: use menos letras con tilde o símbolos.')
      : null,
    needsLyrics && lyricsBytes > LYRICS_MAX_BYTES ? t('The lyrics are too long (8 KB at most).', 'La letra es demasiado larga (8 KB como máximo).') : null,
    sample && sample.size > SAMPLE_MAX_BYTES ? t('The sample is too big (20 MB at most).', 'La muestra es demasiado grande (20 MB como máximo).') : null,
    !Number.isInteger(durationN) || durationN < 10 || durationN > 240 ? t('The length is 10 to 240 seconds.', 'La duración es de 10 a 240 segundos.') : null,
    seedN !== undefined && (!Number.isInteger(seedN) || seedN < 0 || seedN > 2147483647) ? t('The seed is a whole number from 0.', 'La semilla es un número entero desde 0.') : null,
    strengthN !== undefined && (!Number.isFinite(strengthN) || strengthN < 0 || strengthN > 1) ? t('The strength is 0 to 1.', 'La intensidad es de 0 a 1.') : null,
  ].filter((issue): issue is string => issue !== null);
  const missing = [
    description.trim() === '' ? t('Describe your song.', 'Describa su canción.') : null,
    (lyricsMode === 'mine' || lyricsMode === 'ai') && lyrics.trim() === ''
      ? lyricsMode === 'ai'
        ? t('Press "Write the lyrics", or write them yourself.', 'Pulse "Escribir la letra", o escríbala usted.')
        : t('Write your lyrics, or choose "No vocals".', 'Escriba su letra, o elija "Sin voz".')
      : null,
  ].filter((item): item is string => item !== null);
  const canSave = issues.length === 0 && description.trim() !== '' && busy === null;
  const canMake = ready && nodeId !== null && issues.length === 0 && missing.length === 0 && busy === null;

  const promptInput = (): MusicPromptInput => ({
    title: title.trim(),
    description,
    lyricsMode,
    lyrics,
    durationS: durationN,
    language,
    seed: seedN ?? null,
    variations: Number(variations),
    ...(hasSample ? { sampleMode: mode, strength: strengthN ?? null } : {}),
    uiLanguage: lang,
  });

  const run = async (what: NonNullable<typeof busy>, action: () => Promise<void>) => {
    setBusy(what);
    setProblem(null);
    setNotice(null);
    try {
      await action();
    } catch (error) {
      setProblem(refusalText(error, t));
    } finally {
      setBusy(null);
    }
  };

  const make = (event: FormEvent) => {
    event.preventDefault();
    if (!canMake || !nodeId) return;
    void run('make', async () => {
      const request = await createMusicRequest({
        nodeId,
        title: title.trim(),
        description,
        lyricsMode,
        lyrics,
        uiLanguage: lang,
        ...(prompt ? { promptId: prompt.id } : {}),
        ...(prompt?.hasSample && removeSaved ? { removeSample: true } : {}),
        durationS: durationN,
        language,
        variations: Number(variations),
        ...(seedN !== undefined ? { seed: seedN } : {}),
        ...(sample ? { sample } : {}),
        ...(hasSample ? { keepsSavedSample: sample === null, sampleMode: mode, ...(strengthN !== undefined ? { strength: strengthN } : {}) } : {}),
      });
      onCreated(request);
    });
  };

  const save = () =>
    void run('save', async () => {
      const saved = prompt ? await updatePrompt(prompt.id, { ...promptInput(), ...(prompt.hasSample && removeSaved ? { removeSample: true as const } : {}) }) : await createPrompt(promptInput());
      setTitle(saved.title);
      setNotice(sample ? t('Saved. The new sample is kept when you make the song.', 'Guardado. La muestra nueva se guarda cuando cree la canción.') : t('Saved.', 'Guardado.'));
      onSaved(saved);
    });

  const saveAsNew = () =>
    void run('copy', async () => {
      const copy = await createPrompt({ ...promptInput(), ...(savedSample && prompt ? { copySampleFrom: prompt.id } : {}) });
      onSaved(copy);
    });

  const suggest = () =>
    void run('title', async () => {
      const answer = await suggestTitle({ lyrics, description, lyricsMode, language, uiLanguage: lang });
      setTitle(answer.title);
      setTitleNote(answer.source === 'ai' ? t('Suggested by the AI. You can change it.', 'Sugerido por la IA. Puede cambiarlo.') : t('Made from your words. You can change it.', 'Hecho con sus palabras. Puede cambiarlo.'));
    });

  const writeLyrics = () =>
    void run('lyrics', async () => {
      const answer = await suggestLyrics({ description, language, ...(title.trim() ? { title: title.trim() } : {}) });
      setLyrics(answer.lyrics);
      setNotice(t('The AI wrote these lyrics. Read and change them before you make the song.', 'La IA escribió esta letra. Léala y cámbiela antes de crear la canción.'));
    });

  return (
    <Box component="form" onSubmit={make} aria-labelledby="music-form-title" noValidate sx={{ display: 'flex', flexDirection: 'column', gap: 2, minWidth: 0 }}>
      <Typography id="music-form-title" variant="h6" component="h2">
        {prompt ? t(`Prompt: ${prompt.title}`, `Indicación: ${prompt.title}`) : t('New song', 'Canción nueva')}
      </Typography>
      <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 1, alignItems: 'flex-start' }}>
        <TextField
          label={t('Title', 'Título')}
          value={title}
          onChange={(event) => {
            setTitle(event.target.value);
            setTitleNote(null);
          }}
          sx={{ flex: '1 1 16rem', minWidth: 0 }}
          helperText={titleNote ?? t('Leave it empty and one is made from your words.', 'Déjelo vacío y se hace uno con sus palabras.')}
          error={charCount(title.trim()) > TITLE_MAX_CHARS}
          slotProps={{ htmlInput: { maxLength: TITLE_MAX_CHARS + 20 } }}
        />
        <Button variant="outlined" onClick={suggest} disabled={busy !== null || (description.trim() === '' && lyrics.trim() === '')} sx={{ mt: 1 }}>
          {busy === 'title' ? t('Thinking…', 'Pensando…') : t('Suggest a title', 'Sugerir un título')}
        </Button>
      </Box>
      <TextField
        label={t('Describe your song', 'Describa su canción')}
        value={description}
        onChange={(event) => setDescription(event.target.value)}
        multiline
        minRows={2}
        fullWidth
        required
        placeholder={t('a slow ballad about the sea, soft piano, warm voice, 80 bpm', 'una balada lenta sobre el mar, piano suave, voz cálida, 80 bpm')}
        helperText={`${descriptionChars} / ${DESCRIPTION_MAX_CHARS} ${t('characters. Say the style, the instruments, the voice and the mood.', 'caracteres. Diga el estilo, los instrumentos, la voz y el ánimo.')}`}
        error={descriptionChars > DESCRIPTION_MAX_CHARS || utf8Bytes(description) > TAGS_MAX_BYTES}
      />
      <FormControl>
        <FormLabel id="music-lyrics-mode">{t('Lyrics', 'Letra')}</FormLabel>
        <RadioGroup aria-labelledby="music-lyrics-mode" value={lyricsMode} onChange={(event) => setLyricsMode(event.target.value as LyricsMode)}>
          <FormControlLabel value="auto" control={<Radio />} label={t('Automatic (recommended)', 'Automático (recomendado)')} />
          <FormControlLabel value="mine" control={<Radio />} label={t('Use my lyrics', 'Usar mi letra')} />
          <FormControlLabel value="ai" control={<Radio />} label={t('Write lyrics now', 'Escribir la letra ahora')} disabled={!aiOn} />
          <FormControlLabel value="instrumental" control={<Radio />} label={t('No vocals (instrumental)', 'Sin voz (instrumental)')} />
        </RadioGroup>
        {!aiOn ? <FormHelperText data-testid="ai-off-reason">{musicAiOffText(assist, t)}</FormHelperText> : null}
      </FormControl>
      {lyricsMode === 'ai' ? (
        <Box>
          <Button variant="outlined" onClick={writeLyrics} disabled={busy !== null || description.trim() === '' || !aiOn}>
            {busy === 'lyrics' ? t('Writing…', 'Escribiendo…') : lyrics.trim() === '' ? t('Write the lyrics', 'Escribir la letra') : t('Write them again', 'Escribirla otra vez')}
          </Button>
        </Box>
      ) : null}
      {needsLyrics ? (
        <TextField
          label={t('Your lyrics', 'Su letra')}
          value={lyrics}
          onChange={(event) => setLyrics(event.target.value)}
          multiline
          minRows={6}
          fullWidth
          helperText={`${lyricsBytes} / ${LYRICS_MAX_BYTES} ${t('bytes. Use [verse] and [chorus] to mark the parts.', 'bytes. Use [verse] y [chorus] para marcar las partes.')}`}
          error={lyricsBytes > LYRICS_MAX_BYTES}
        />
      ) : null}
      {lyricsMode === 'auto' ? (
        <Typography variant="body2" color="text.secondary" data-testid="lyrics-hint" aria-live="polite">
          {autoLyricsHint(assist, lyrics, t)}
        </Typography>
      ) : null}
      {!needsLyrics ? (
        <Typography color="text.secondary">{t('The song is made without singing.', 'La canción se hace sin canto.')}</Typography>
      ) : null}
      <Box sx={{ display: 'flex', flexDirection: 'column', gap: 1 }}>
        <FormLabel htmlFor={sampleId}>{t('Sample (optional): a song or a hum, up to 20 MB', 'Muestra (opcional): una canción o un tarareo, hasta 20 MB')}</FormLabel>
        {savedSample && !sample ? (
          <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 1, alignItems: 'center' }} data-testid="saved-sample">
            <Typography variant="body2">
              {t('This prompt has a saved sample', 'Esta indicación tiene una muestra guardada')} {prompt?.sampleBytes ? `(${formatBytes(prompt.sampleBytes)})` : ''}
            </Typography>
            <Button size="small" onClick={() => setRemoveSaved(true)}>
              {t('Remove sample', 'Quitar la muestra')}
            </Button>
          </Box>
        ) : null}
        <input id={sampleId} type="file" accept={SAMPLE_TYPES} onChange={(event) => setSample(event.target.files?.[0] ?? null)} style={{ maxWidth: '100%' }} />
        {hasSample ? (
          <FormControl>
            <FormLabel id="music-sample-mode">{t('Use the sample to…', 'Usar la muestra para…')}</FormLabel>
            <RadioGroup aria-labelledby="music-sample-mode" value={mode} onChange={(event) => setMode(event.target.value as SampleMode)}>
              <FormControlLabel value="sounds_like" control={<Radio />} label={t('Sounds like this', 'Que suene así')} />
              <FormControlLabel value="same_song" control={<Radio />} label={t('Same song, my lyrics', 'La misma canción, mi letra')} />
            </RadioGroup>
          </FormControl>
        ) : null}
      </Box>
      <Accordion disableGutters>
        <AccordionSummary expandIcon={<ExpandMoreIcon />}>{t('More options', 'Más opciones')}</AccordionSummary>
        <AccordionDetails sx={{ display: 'flex', flexWrap: 'wrap', gap: 2 }}>
          <TextField label={t('Length (seconds)', 'Duración (segundos)')} type="number" value={duration} onChange={(event) => setDuration(event.target.value)} slotProps={{ htmlInput: { min: 10, max: 240 } }} sx={{ width: 170 }} />
          <TextField select label={t('Language', 'Idioma')} value={language} onChange={(event) => setLanguage(event.target.value)} sx={{ width: 150 }}>
            <MenuItem value="en">English</MenuItem>
            <MenuItem value="es">Español</MenuItem>
          </TextField>
          <TextField select label={t('Versions', 'Versiones')} value={variations} onChange={(event) => setVariations(event.target.value)} sx={{ width: 120 }}>
            {['1', '2', '3', '4'].map((count) => (
              <MenuItem key={count} value={count}>
                {count}
              </MenuItem>
            ))}
          </TextField>
          <TextField label={t('Seed (optional)', 'Semilla (opcional)')} value={seed} onChange={(event) => setSeed(event.target.value)} sx={{ width: 160 }} />
          {hasSample ? (
            <TextField label={t('Strength, 0 to 1 (optional)', 'Intensidad, 0 a 1 (opcional)')} value={strength} onChange={(event) => setStrength(event.target.value)} sx={{ width: 200 }} />
          ) : null}
        </AccordionDetails>
      </Accordion>
      {issues.length > 0 ? (
        <Alert severity="warning">
          {issues.map((issue) => (
            <div key={issue}>{issue}</div>
          ))}
        </Alert>
      ) : null}
      {missing.length > 0 && (description !== '' || lyrics !== '') ? (
        <Typography color="text.secondary" data-testid="form-missing">
          {missing.join(' ')}
        </Typography>
      ) : null}
      {!ready ? <Typography color="text.secondary">{t('Choose a computer that is ready for music.', 'Elija una computadora lista para música.')}</Typography> : null}
      {problem ? (
        <Alert severity="error" role="alert">
          {problem}
        </Alert>
      ) : null}
      {notice ? (
        <Alert severity="info" role="status" onClose={() => setNotice(null)}>
          {notice}
        </Alert>
      ) : null}
      <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 1 }}>
        <Button type="submit" variant="contained" disabled={!canMake}>
          {busy === 'make' ? t('Sending…', 'Enviando…') : t('Make the song', 'Crear la canción')}
        </Button>
        <Button variant="outlined" onClick={save} disabled={!canSave}>
          {busy === 'save' ? t('Saving…', 'Guardando…') : t('Save', 'Guardar')}
        </Button>
        {prompt ? (
          <>
            <Button onClick={saveAsNew} disabled={!canSave}>
              {t('Save as new', 'Guardar como nueva')}
            </Button>
            <Button color="error" onClick={() => setConfirmDelete(true)} disabled={busy !== null}>
              {t('Delete', 'Eliminar')}
            </Button>
          </>
        ) : null}
      </Box>
      {prompt ? (
        <ConfirmDialog
          open={confirmDelete}
          title={t('Delete this prompt?', '¿Eliminar esta indicación?')}
          confirmLabel={t('Delete', 'Eliminar')}
          destructive
          onClose={() => setConfirmDelete(false)}
          onConfirm={async () => {
            try {
              await deletePrompt(prompt.id);
              setConfirmDelete(false);
              onDeleted(prompt.id);
              return null;
            } catch (error) {
              return refusalText(error, t);
            }
          }}
        >
          {t(
            `"${prompt.title}" is removed from My prompts. Its ${prompt.songCount === 1 ? 'song stays' : `${prompt.songCount} songs stay`} in the library.`,
            `"${prompt.title}" se quita de Mis indicaciones. ${prompt.songCount === 1 ? 'Su canción se queda' : `Sus ${prompt.songCount} canciones se quedan`} en la biblioteca.`,
          )}
        </ConfirmDialog>
      ) : null}
    </Box>
  );
}
