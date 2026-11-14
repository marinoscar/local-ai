import { useEffect, useId, useRef, useState, type FormEvent } from 'react';

import { ErrorNotice } from '../common/ErrorNotice';
import { toErrorInfo, type TransportErrorInfo } from '../common/errors';
import { useStrings } from '../common/strings';
import { useLang } from '../lang';
import { formatBytes } from './format';
import { MUSIC_STRINGS, SONG_LANGUAGES, type MusicKey } from './strings';
import type { MusicOp, MusicTransport, SampleMode } from './types';
import {
  CODE_INPUT_REJECTED,
  DURATION_DEFAULT_S,
  DURATION_MAX_S,
  DURATION_MIN_S,
  SAMPLE_ACCEPT,
  SECTION_TAGS,
  VARIATIONS_MAX,
  defaultStrength,
  insertSection,
  toRequest,
  validateForm,
  type FieldError,
  type FieldName,
  type StudioForm,
} from './validate';

/** The code of a song that failed without a code of its own: the engine stopped during the song. */
export const CODE_SONG_FAILED = 'LAI-SVC-152';
/** The code of a cancelled song (info). */
export const CODE_SONG_CANCELLED = 'LAI-SVC-158';

export interface MusicStudioProps {
  transport: Pick<MusicTransport, 'generate' | 'watch' | 'cancel'>;
  /** True while music is not ready: the form stays readable and editable, Generate is off and says why. */
  disabled?: boolean;
  /** Called with the new song ids when a generation finished (the host refreshes its SongLibrary). */
  onSongsCreated?: (songIds: string[]) => void;
  /** The singing language selected at first (default `en`). */
  defaultLanguage?: string;
  /** Heading level of the title (default 2). */
  headingLevel?: 2 | 3 | 4;
  className?: string;
}

type Phase =
  | { kind: 'idle' }
  | { kind: 'sending' }
  | { kind: 'running'; opId: string; op: MusicOp | null; cancelling: boolean }
  | { kind: 'done'; songIds: string[] }
  | { kind: 'failed'; error: TransportErrorInfo }
  | { kind: 'cancelled' };

/**
 * MusicStudio is the song form: lyrics with section buttons, style, length, language, an optional sample with the
 * plain choice "Sounds like this" / "Same song, my lyrics", "More options" (strength, seed, versions), then Generate
 * with progress and Cancel. It validates before sending (the node checks again) and shows every failure with its
 * doctor code. It owns no data: everything goes through `transport`.
 */
export function MusicStudio({ transport, disabled = false, onSongsCreated, defaultLanguage = 'en', headingLevel = 2, className }: MusicStudioProps) {
  const t = useStrings(MUSIC_STRINGS);
  const lang = useLang();
  const id = useId();
  const lyricsRef = useRef<HTMLTextAreaElement>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  const [form, setForm] = useState<StudioForm>({
    lyrics: '',
    tags: '',
    duration: String(DURATION_DEFAULT_S),
    language: (SONG_LANGUAGES as readonly string[]).includes(defaultLanguage) ? defaultLanguage : 'en',
    seed: '',
    variations: '1',
    sample: null,
    sampleMode: 'sounds_like',
    strength: null,
  });
  const [errors, setErrors] = useState<FieldError[]>([]);
  const [phase, setPhase] = useState<Phase>({ kind: 'idle' });
  const onCreated = useRef(onSongsCreated);
  onCreated.current = onSongsCreated;

  const update = <K extends keyof StudioForm>(k: K, v: StudioForm[K]) => setForm((f) => ({ ...f, [k]: v }));

  const runningOp = phase.kind === 'running' ? phase.opId : null;
  useEffect(() => {
    if (!runningOp) return;
    let ended = false;
    const stop = transport.watch(runningOp, (op) => {
      if (ended) return;
      if (op.state === 'done') {
        ended = true;
        setPhase({ kind: 'done', songIds: op.song_ids });
        onCreated.current?.(op.song_ids);
      } else if (op.state === 'failed') {
        ended = true;
        setPhase({ kind: 'failed', error: toErrorInfo(op.error ?? {}, CODE_SONG_FAILED) });
      } else if (op.state === 'cancelled') {
        ended = true;
        setPhase({ kind: 'cancelled' });
      } else {
        setPhase((p) => (p.kind === 'running' && p.opId === runningOp ? { ...p, op } : p));
      }
    });
    return () => {
      ended = true;
      stop();
    };
  }, [runningOp, transport]);

  const busy = phase.kind === 'sending' || phase.kind === 'running';

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (busy || disabled) return;
    const errs = validateForm(form);
    setErrors(errs);
    if (errs.length > 0) {
      setPhase({ kind: 'idle' });
      // Focus the first field with a problem, so a keyboard user lands on it.
      const first = document.getElementById(`${id}-${errs[0]?.field}`);
      first?.focus();
      return;
    }
    setPhase({ kind: 'sending' });
    try {
      const acc = await transport.generate(toRequest(form));
      setPhase({ kind: 'running', opId: acc.op_id, op: null, cancelling: false });
    } catch (err) {
      setPhase({ kind: 'failed', error: toErrorInfo(err, CODE_INPUT_REJECTED) });
    }
  };

  const cancel = async () => {
    if (phase.kind !== 'running' || phase.cancelling) return;
    const opId = phase.opId;
    setPhase({ ...phase, cancelling: true });
    try {
      await transport.cancel(opId);
    } catch (err) {
      setPhase({ kind: 'failed', error: toErrorInfo(err, CODE_SONG_FAILED) });
    }
  };

  const addSection = (tag: (typeof SECTION_TAGS)[number]) => {
    const el = lyricsRef.current;
    const start = el?.selectionStart ?? form.lyrics.length;
    const end = el?.selectionEnd ?? start;
    const next = insertSection(form.lyrics, start, end, tag);
    update('lyrics', next.text);
    requestAnimationFrameSafe(() => {
      if (!el) return;
      el.focus();
      el.setSelectionRange(next.caret, next.caret);
    });
  };

  const chooseSample = (file: File | null) => {
    setForm((f) => ({ ...f, sample: file, strength: null }));
  };
  const removeSample = () => {
    chooseSample(null);
    if (fileRef.current) fileRef.current.value = '';
    fileRef.current?.focus();
  };
  const setMode = (m: SampleMode) => setForm((f) => ({ ...f, sampleMode: m, strength: null }));

  const errOf = (field: FieldName) => errors.find((x) => x.field === field);
  const fieldProps = (field: FieldName, hintId?: string) => {
    const err = errOf(field);
    const describedBy = [hintId, err ? `${id}-${field}-err` : undefined].filter(Boolean).join(' ') || undefined;
    return { id: `${id}-${field}`, 'aria-invalid': err ? true : undefined, 'aria-describedby': describedBy };
  };
  const fieldError = (field: FieldName) => {
    const err = errOf(field);
    return err ? (
      <p id={`${id}-${field}-err`} className="lai-field-error">
        {t(err.key as MusicKey, err.vars)}
      </p>
    ) : null;
  };

  const strength = form.strength ?? defaultStrength(form.sampleMode);
  const H = `h${headingLevel}` as 'h2';
  const pct = phase.kind === 'running' && phase.op?.progress != null ? Math.round(phase.op.progress * 100) : null;

  return (
    <section className={`lai-studio${className ? ` ${className}` : ''}`} aria-labelledby={`${id}-title`}>
      <H id={`${id}-title`} className="lai-studio-title">
        {t('studioTitle')}
      </H>
      <form className="lai-studio-form" onSubmit={(e) => void submit(e)} noValidate>
        {errors.length > 0 ? <ErrorNotice error={{ code: CODE_INPUT_REJECTED, message: t('fixFields') }} /> : null}

        <div className="lai-field">
          <label htmlFor={`${id}-lyrics`}>{t('lyrics')}</label>
          <p id={`${id}-lyrics-hint`} className="lai-hint">
            {t('lyricsHint')}
          </p>
          <div className="lai-tag-buttons" role="group" aria-label={t('sections')}>
            {SECTION_TAGS.map((tag) => (
              <button key={tag} type="button" className="lai-btn lai-btn-small" onClick={() => addSection(tag)} data-section={tag}>
                {t('insertTag', { tag: t(`tag_${tag}` as MusicKey) })}
              </button>
            ))}
          </div>
          <textarea
            ref={lyricsRef}
            rows={10}
            value={form.lyrics}
            onChange={(e) => update('lyrics', e.target.value)}
            {...fieldProps('lyrics', `${id}-lyrics-hint`)}
          />
          {fieldError('lyrics')}
        </div>

        <div className="lai-field">
          <label htmlFor={`${id}-tags`}>{t('tags')}</label>
          <p id={`${id}-tags-hint`} className="lai-hint">
            {t('tagsHint')}
          </p>
          <input type="text" value={form.tags} onChange={(e) => update('tags', e.target.value)} {...fieldProps('tags', `${id}-tags-hint`)} />
          {fieldError('tags')}
        </div>

        <div className="lai-field-row">
          <div className="lai-field">
            <label htmlFor={`${id}-duration_s`}>{t('duration')}</label>
            <p id={`${id}-duration-hint`} className="lai-hint">
              {t('durationHint', { min: DURATION_MIN_S, max: DURATION_MAX_S })}
            </p>
            <input
              type="number"
              inputMode="numeric"
              min={DURATION_MIN_S}
              max={DURATION_MAX_S}
              step={1}
              value={form.duration}
              onChange={(e) => update('duration', e.target.value)}
              {...fieldProps('duration_s', `${id}-duration-hint`)}
            />
            {fieldError('duration_s')}
          </div>
          <div className="lai-field">
            <label htmlFor={`${id}-language`}>{t('language')}</label>
            <select value={form.language} onChange={(e) => update('language', e.target.value)} {...fieldProps('language')}>
              {SONG_LANGUAGES.map((l) => (
                <option key={l} value={l}>
                  {t(`lang_${l}` as MusicKey)}
                </option>
              ))}
            </select>
          </div>
        </div>

        <div className="lai-field">
          <label htmlFor={`${id}-sample`}>{t('sample')}</label>
          <p id={`${id}-sample-hint`} className="lai-hint">
            {t('sampleHint')}
          </p>
          <input
            ref={fileRef}
            type="file"
            accept={SAMPLE_ACCEPT}
            onChange={(e) => chooseSample(e.target.files?.[0] ?? null)}
            {...fieldProps('sample', `${id}-sample-hint`)}
          />
          {fieldError('sample')}
          {form.sample ? (
            <>
              <p className="lai-sample-chosen">
                {t('sampleChosen', { name: form.sample.name, size: formatBytes(form.sample.size, lang) })}{' '}
                <button type="button" className="lai-btn lai-btn-small" onClick={removeSample}>
                  {t('sampleRemove')}
                </button>
              </p>
              <fieldset className="lai-sample-mode">
                <legend>{t('sampleUse')}</legend>
                {(['sounds_like', 'same_song'] as const).map((m) => (
                  <div key={m} className="lai-radio">
                    <input
                      type="radio"
                      id={`${id}-mode-${m}`}
                      name={`${id}-mode`}
                      value={m}
                      checked={form.sampleMode === m}
                      onChange={() => setMode(m)}
                      aria-describedby={`${id}-mode-${m}-hint`}
                    />
                    <label htmlFor={`${id}-mode-${m}`}>{t(`mode_${m}` as MusicKey)}</label>
                    <p id={`${id}-mode-${m}-hint`} className="lai-hint">
                      {t(`mode_${m}_hint` as MusicKey)}
                    </p>
                  </div>
                ))}
              </fieldset>
            </>
          ) : null}
        </div>

        <details className="lai-more" open={errors.some((x) => x.field === 'seed' || x.field === 'variations' || x.field === 'strength') || undefined}>
          <summary>{t('more')}</summary>
          <div className="lai-field">
            <label htmlFor={`${id}-strength`}>{t('strength', { pct: Math.round(strength * 100) })}</label>
            <p id={`${id}-strength-hint`} className="lai-hint">
              {form.sample ? t('strengthHint') : t('strengthNoSample')}
            </p>
            <input
              type="range"
              min={0}
              max={1}
              step={0.05}
              value={strength}
              disabled={!form.sample}
              onChange={(e) => update('strength', Number(e.target.value))}
              {...fieldProps('strength', `${id}-strength-hint`)}
            />
            {fieldError('strength')}
          </div>
          <div className="lai-field">
            <label htmlFor={`${id}-seed`}>{t('seed')}</label>
            <p id={`${id}-seed-hint`} className="lai-hint">
              {t('seedHint')}
            </p>
            <input type="text" inputMode="numeric" value={form.seed} onChange={(e) => update('seed', e.target.value)} {...fieldProps('seed', `${id}-seed-hint`)} />
            {fieldError('seed')}
          </div>
          <div className="lai-field">
            <label htmlFor={`${id}-variations`}>{t('variations')}</label>
            <p id={`${id}-variations-hint`} className="lai-hint">
              {t('variationsHint', { max: VARIATIONS_MAX })}
            </p>
            <select value={form.variations} onChange={(e) => update('variations', e.target.value)} {...fieldProps('variations', `${id}-variations-hint`)}>
              {Array.from({ length: VARIATIONS_MAX }, (_, i) => String(i + 1)).map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </select>
            {fieldError('variations')}
          </div>
        </details>

        <div className="lai-actions">
          <button type="submit" className="lai-btn lai-btn-primary" disabled={busy || disabled} aria-describedby={disabled ? `${id}-disabled` : undefined}>
            {t('generate')}
          </button>
          {phase.kind === 'running' ? (
            <button type="button" className="lai-btn" onClick={() => void cancel()} disabled={phase.cancelling}>
              {phase.cancelling ? t('cancelling') : t('cancel')}
            </button>
          ) : null}
        </div>
        {disabled ? (
          <p id={`${id}-disabled`} className="lai-hint">
            {t('disabledHint')}
          </p>
        ) : null}

        <div className="lai-studio-state" aria-live="polite">
          {phase.kind === 'sending' ? <p>{t('sending')}</p> : null}
          {phase.kind === 'running' ? (
            <div className="lai-studio-progress" data-testid="studio-progress">
              <p>{phase.op === null || phase.op.state === 'queued' ? t('queued') : pct === null ? t('runningNoPct') : t('running', { pct })}</p>
              {pct === null ? <progress aria-label={t('progressLabel')} /> : <progress aria-label={t('progressLabel')} max={100} value={pct} />}
            </div>
          ) : null}
          {phase.kind === 'done' ? (
            <p className="lai-studio-done">{phase.songIds.length > 1 ? t('doneMany', { n: phase.songIds.length }) : t('done')}</p>
          ) : null}
        </div>
        {phase.kind === 'failed' ? <ErrorNotice error={phase.error} /> : null}
        {phase.kind === 'cancelled' ? <ErrorNotice role="status" error={{ code: CODE_SONG_CANCELLED, message: t('cancelled') }} /> : null}
      </form>
    </section>
  );
}

function requestAnimationFrameSafe(fn: () => void) {
  if (typeof window !== 'undefined' && typeof window.requestAnimationFrame === 'function') window.requestAnimationFrame(fn);
  else fn();
}
