import { useCallback, useEffect, useId, useRef, useState } from 'react';

import { ErrorNotice } from '../common/ErrorNotice';
import { toErrorInfo, type TransportErrorInfo } from '../common/errors';
import { useStrings } from '../common/strings';
import { useLang } from '../lang';
import { fileNameOf, formatDate, formatDuration } from './format';
import { MUSIC_STRINGS } from './strings';
import type { Song, SongTransport } from './types';

/** The code shown when the list or a delete fails without a code of its own: the service is not answering. */
export const CODE_LIBRARY_FAILED = 'LAI-SVC-116';
/** The answers that mean "the admin PIN is needed" (missing, wrong): the confirmation asks for it. */
const PIN_CODES = new Set(['LAI-SEC-002', 'LAI-SEC-003']);

export interface SongLibraryProps {
  transport: SongTransport;
  /** Change it (a counter, the last song ids) to read the list again, for example after a generation. */
  refreshKey?: unknown;
  /** Songs per page (default 20). */
  pageSize?: number;
  /** Heading level of the title (default 2); each song's name is one level below. */
  headingLevel?: 2 | 3;
  /** IANA zone for the dates (tests pin it). Default: the viewer's. */
  timeZone?: string;
  className?: string;
}

/**
 * SongLibrary lists the songs with an inline player, a download link and Delete. Delete always asks first, in words
 * that say it cannot be undone, with "No, keep it" focused (AGENTS.md rule 3); a refusal (for example the admin PIN,
 * LAI-SEC-002) is shown with its doctor code.
 */
export function SongLibrary({ transport, refreshKey, pageSize = 20, headingLevel = 2, timeZone, className }: SongLibraryProps) {
  const t = useStrings(MUSIC_STRINGS);
  const titleId = useId();
  const headingRef = useRef<HTMLHeadingElement>(null);
  const [songs, setSongs] = useState<Song[] | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [error, setError] = useState<TransportErrorInfo | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [notice, setNotice] = useState('');

  const load = useCallback(async () => {
    try {
      const page = await transport.listSongs({ limit: pageSize });
      setSongs(page.songs);
      setCursor(page.next_cursor);
      setError(null);
    } catch (e) {
      setError(toErrorInfo(e, CODE_LIBRARY_FAILED));
    }
  }, [transport, pageSize]);

  useEffect(() => {
    void load();
  }, [load, refreshKey]);

  const more = async () => {
    if (!cursor) return;
    setLoadingMore(true);
    try {
      const page = await transport.listSongs({ cursor, limit: pageSize });
      setSongs((s) => {
        const seen = new Set((s ?? []).map((x) => x.id));
        return [...(s ?? []), ...page.songs.filter((x) => !seen.has(x.id))];
      });
      setCursor(page.next_cursor);
    } catch (e) {
      setError(toErrorInfo(e, CODE_LIBRARY_FAILED));
    } finally {
      setLoadingMore(false);
    }
  };

  const removed = (id: string) => {
    setSongs((s) => (s ?? []).filter((x) => x.id !== id));
    setNotice(t('deleted'));
    headingRef.current?.focus();
  };

  const H = `h${headingLevel}` as 'h2';
  const itemLevel = (headingLevel + 1) as 3 | 4;

  return (
    <section className={`lai-library${className ? ` ${className}` : ''}`} aria-labelledby={titleId}>
      <H id={titleId} ref={headingRef} tabIndex={-1} className="lai-library-title">
        {t('libraryTitle')}
      </H>
      <p className="lai-visually-hidden" role="status">
        {notice}
      </p>
      {songs === null && !error ? <p>{t('libraryLoading')}</p> : null}
      {error ? (
        <>
          {songs === null ? <p>{t('libraryFailed')}</p> : null}
          <ErrorNotice error={error} />
          <button type="button" className="lai-btn" onClick={() => void load()}>
            {t('retry')}
          </button>
        </>
      ) : null}
      {songs !== null && songs.length === 0 ? <p className="lai-library-empty">{t('libraryEmpty')}</p> : null}
      {songs !== null && songs.length > 0 ? (
        <ul className="lai-songs">
          {songs.map((s) => (
            <SongItem key={s.id} song={s} transport={transport} level={itemLevel} timeZone={timeZone} onDeleted={removed} />
          ))}
        </ul>
      ) : null}
      {cursor ? (
        <button type="button" className="lai-btn" disabled={loadingMore} onClick={() => void more()}>
          {t('more_songs')}
        </button>
      ) : null}
    </section>
  );
}

function SongItem({
  song,
  transport,
  level,
  timeZone,
  onDeleted,
}: {
  song: Song;
  transport: SongTransport;
  level: 3 | 4;
  timeZone?: string;
  onDeleted: (id: string) => void;
}) {
  const t = useStrings(MUSIC_STRINGS);
  const lang = useLang();
  const id = useId();
  // A plain path (the agent) is known at once; a promise (a signed cloud URL) arrives through the effect below.
  const [url, setUrl] = useState<string | null>(() => {
    try {
      const r = transport.audioUrl(song);
      return typeof r === 'string' ? r : null;
    } catch {
      return null;
    }
  });
  const [urlError, setUrlError] = useState<TransportErrorInfo | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [error, setError] = useState<TransportErrorInfo | null>(null);
  const [pinAsked, setPinAsked] = useState(false);
  const [pin, setPin] = useState('');
  const [pinError, setPinError] = useState<TransportErrorInfo | null>(null);
  const keepRef = useRef<HTMLButtonElement>(null);
  const deleteRef = useRef<HTMLButtonElement>(null);
  const pinRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    let live = true;
    try {
      const r = transport.audioUrl(song);
      if (typeof r === 'string') setUrl(r);
      else
        void r.then(
          (u) => live && setUrl(u),
          (e) => live && setUrlError(toErrorInfo(e, CODE_LIBRARY_FAILED)),
        );
    } catch (e) {
      setUrlError(toErrorInfo(e, CODE_LIBRARY_FAILED));
    }
    return () => {
      live = false;
    };
  }, [song, transport]);

  useEffect(() => {
    if (confirming) keepRef.current?.focus();
  }, [confirming]);

  const title = song.title.trim() || t('noTitle');
  const Hn = `h${level}` as 'h3';

  const doDelete = async () => {
    setDeleting(true);
    setError(null);
    try {
      await transport.deleteSong(song.id, pinAsked ? { pin } : undefined);
      onDeleted(song.id);
    } catch (e) {
      const info = toErrorInfo(e, CODE_LIBRARY_FAILED);
      setDeleting(false);
      if (PIN_CODES.has(info.code)) {
        // The node wants the admin PIN (a wrong one comes back as LAI-SEC-003): ask for it inside the confirmation.
        setPinAsked(true);
        setPinError(info);
        setPin('');
        requestAnimationFrame(() => pinRef.current?.focus());
        return;
      }
      setError(info);
      closeConfirm();
    }
  };

  const closeConfirm = () => {
    setConfirming(false);
    setPinAsked(false);
    setPinError(null);
    setPin('');
  };

  return (
    <li className="lai-song" data-song={song.id}>
      <Hn id={`${id}-title`} className="lai-song-title">
        {title}
      </Hn>
      <p className="lai-song-meta">
        {t('details', { duration: formatDuration(song.duration_s), date: formatDate(song.created_at, lang, timeZone) })}
        {song.tags ? ` · ${song.tags}` : ''}
        {song.origin === 'cloud' ? ` · ${t('fromCloud')}` : ''}
      </p>
      {url ? <audio className="lai-song-audio" controls preload="none" src={url} aria-label={t('play', { title })} /> : null}
      {urlError ? <ErrorNotice error={urlError} /> : null}
      <div className="lai-actions">
        {url ? (
          <a className="lai-btn" href={url} download={fileNameOf(title, song.id, song.mime)} aria-label={t('downloadLabel', { title })}>
            {t('download')}
          </a>
        ) : null}
        <button
          ref={deleteRef}
          type="button"
          className="lai-btn lai-btn-danger"
          aria-label={t('deleteLabel', { title })}
          disabled={confirming || deleting}
          onClick={() => setConfirming(true)}
        >
          {t('delete')}
        </button>
      </div>
      {confirming ? (
        <div className="lai-confirm" role="alertdialog" aria-labelledby={`${id}-confirm`} aria-modal="false">
          <p id={`${id}-confirm`}>{t('deleteConfirm', { title })}</p>
          {pinAsked ? (
            <form
              className="lai-field"
              onSubmit={(e) => {
                e.preventDefault();
                if (pin !== '' && !deleting) void doDelete();
              }}
            >
              {pinError ? <ErrorNotice error={pinError} /> : null}
              <label htmlFor={`${id}-pin`}>{t('pin')}</label>
              <input
                ref={pinRef}
                id={`${id}-pin`}
                type="password"
                inputMode="numeric"
                autoComplete="off"
                maxLength={8}
                value={pin}
                onChange={(e) => setPin(e.target.value.replace(/\D/g, ''))}
              />
            </form>
          ) : null}
          <div className="lai-actions">
            <button type="button" className="lai-btn lai-btn-danger" disabled={deleting || (pinAsked && pin === '')} onClick={() => void doDelete()}>
              {deleting ? t('deleting') : t('deleteYes')}
            </button>
            <button
              ref={keepRef}
              type="button"
              className="lai-btn"
              disabled={deleting}
              onClick={() => {
                closeConfirm();
                requestAnimationFrame(() => deleteRef.current?.focus());
              }}
            >
              {t('deleteNo')}
            </button>
          </div>
        </div>
      ) : null}
      {error ? <ErrorNotice error={error} /> : null}
    </li>
  );
}
