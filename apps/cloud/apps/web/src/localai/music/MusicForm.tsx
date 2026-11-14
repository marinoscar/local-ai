// The "make a song" form (docs/specs/cloud-app.md section 3.11, music.md section 3.7, #514): lyrics, style, an optional
// sample with "Sounds like this" / "Same song, my lyrics", and More options (length, language, versions, seed,
// strength). The limits are the API's (lyrics 8 KiB, style 512 bytes, sample 20 MB), checked here first so nothing is
// uploaded in vain; the API checks again.
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
  FormLabel,
  MenuItem,
  Radio,
  RadioGroup,
  TextField,
  Typography,
} from '@mui/material';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import { useId, useState, type FormEvent } from 'react';

import { useT } from '../../i18n/LanguageProvider';
import {
  LYRICS_MAX_BYTES,
  SAMPLE_MAX_BYTES,
  TAGS_MAX_BYTES,
  createMusicRequest,
  utf8Bytes,
  type MusicRequest,
  type SampleMode,
} from './musicApi';
import { refusalText } from './musicText';

export interface MusicFormProps {
  nodeId: string | null;
  /** The chosen node can make music now (its readiness is `ready`). */
  ready: boolean;
  onCreated: (request: MusicRequest) => void;
}

const SAMPLE_TYPES = 'audio/wav,audio/x-wav,audio/mpeg,audio/flac,audio/ogg,.wav,.mp3,.flac,.ogg';

export function MusicForm({ nodeId, ready, onCreated }: MusicFormProps) {
  const t = useT();
  const sampleId = useId();
  const [lyrics, setLyrics] = useState('');
  const [tags, setTags] = useState('');
  const [sample, setSample] = useState<File | null>(null);
  const [mode, setMode] = useState<SampleMode>('sounds_like');
  const [duration, setDuration] = useState('60');
  const [language, setLanguage] = useState('en');
  const [variations, setVariations] = useState('1');
  const [seed, setSeed] = useState('');
  const [strength, setStrength] = useState('');
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);

  const lyricsBytes = utf8Bytes(lyrics);
  const tagsBytes = utf8Bytes(tags);
  const durationN = Number(duration);
  const seedN = seed.trim() === '' ? undefined : Number(seed);
  const strengthN = strength.trim() === '' ? undefined : Number(strength);
  const issues = [
    lyricsBytes > LYRICS_MAX_BYTES ? t('The lyrics are too long (8 KB at most).', 'La letra es demasiado larga (8 KB como máximo).') : null,
    tagsBytes > TAGS_MAX_BYTES ? t('The style is too long (512 characters at most).', 'El estilo es demasiado largo (512 caracteres como máximo).') : null,
    lyrics.trim() === '' && tags.trim() === '' ? t('Write some lyrics or a style.', 'Escriba una letra o un estilo.') : null,
    sample && sample.size > SAMPLE_MAX_BYTES ? t('The sample is too big (20 MB at most).', 'La muestra es demasiado grande (20 MB como máximo).') : null,
    !Number.isInteger(durationN) || durationN < 10 || durationN > 240 ? t('The length is 10 to 240 seconds.', 'La duración es de 10 a 240 segundos.') : null,
    seedN !== undefined && (!Number.isInteger(seedN) || seedN < 0 || seedN > 2147483647) ? t('The seed is a whole number from 0.', 'La semilla es un número entero desde 0.') : null,
    strengthN !== undefined && (!Number.isFinite(strengthN) || strengthN < 0 || strengthN > 1) ? t('The strength is 0 to 1.', 'La intensidad es de 0 a 1.') : null,
  ].filter((issue): issue is string => issue !== null);
  const blank = lyrics.trim() === '' && tags.trim() === '';

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!nodeId || !ready || issues.length > 0) return;
    setBusy(true);
    setProblem(null);
    try {
      const request = await createMusicRequest({
        nodeId,
        lyrics,
        tags,
        durationS: durationN,
        language,
        variations: Number(variations),
        ...(seedN !== undefined ? { seed: seedN } : {}),
        ...(sample ? { sample, sampleMode: mode, ...(strengthN !== undefined ? { strength: strengthN } : {}) } : {}),
      });
      onCreated(request);
    } catch (error) {
      setProblem(refusalText(error, t));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Box component="form" onSubmit={(event) => void submit(event)} aria-labelledby="music-form-title" noValidate sx={{ display: 'flex', flexDirection: 'column', gap: 2, minWidth: 0 }}>
      <Typography id="music-form-title" variant="h6" component="h2">
        {t('Your song', 'Su canción')}
      </Typography>
      <TextField
        label={t('Lyrics', 'Letra')}
        value={lyrics}
        onChange={(event) => setLyrics(event.target.value)}
        multiline
        minRows={6}
        fullWidth
        helperText={`${lyricsBytes} / ${LYRICS_MAX_BYTES} ${t('bytes. Use [verse] and [chorus] to mark the parts.', 'bytes. Use [verse] y [chorus] para marcar las partes.')}`}
        error={lyricsBytes > LYRICS_MAX_BYTES}
      />
      <TextField
        label={t('Style', 'Estilo')}
        value={tags}
        onChange={(event) => setTags(event.target.value)}
        fullWidth
        placeholder={t('folk, acoustic guitar, warm voice', 'folk, guitarra acústica, voz cálida')}
        error={tagsBytes > TAGS_MAX_BYTES}
      />
      <Box sx={{ display: 'flex', flexDirection: 'column', gap: 1 }}>
        <FormLabel htmlFor={sampleId}>{t('Sample (optional): a song or a hum, up to 20 MB', 'Muestra (opcional): una canción o un tarareo, hasta 20 MB')}</FormLabel>
        <input id={sampleId} type="file" accept={SAMPLE_TYPES} onChange={(event) => setSample(event.target.files?.[0] ?? null)} style={{ maxWidth: '100%' }} />
        {sample ? (
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
          {sample ? (
            <TextField label={t('Strength, 0 to 1 (optional)', 'Intensidad, 0 a 1 (opcional)')} value={strength} onChange={(event) => setStrength(event.target.value)} sx={{ width: 200 }} />
          ) : null}
        </AccordionDetails>
      </Accordion>
      {issues.length > 0 && !blank ? (
        <Alert severity="warning">
          {issues.map((issue) => (
            <div key={issue}>{issue}</div>
          ))}
        </Alert>
      ) : null}
      {!ready ? <Typography color="text.secondary">{t('Choose a computer that is ready for music.', 'Elija una computadora lista para música.')}</Typography> : null}
      {problem ? (
        <Alert severity="error" role="alert">
          {problem}
        </Alert>
      ) : null}
      <Box>
        <Button type="submit" variant="contained" disabled={!ready || !nodeId || busy || issues.length > 0}>
          {busy ? t('Sending…', 'Enviando…') : t('Make the song', 'Crear la canción')}
        </Button>
      </Box>
    </Box>
  );
}
