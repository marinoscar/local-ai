import { useCallback, useEffect, useId, useRef, useState } from 'react';

import { ErrorNotice } from '../common/ErrorNotice';
import { toErrorInfo, type TransportErrorInfo } from '../common/errors';
import { useStrings } from '../common/strings';
import { useLang, type Lang } from '../lang';
import { WORKLOAD_STRINGS } from './strings';
import type { Workload, WorkloadProgress, WorkloadsResponse, WorkloadTransport } from './types';

/** The code shown when an activation fails without one of its own: the node restored the previous set. */
export const CODE_WORKLOAD_FAILED = 'LAI-LLM-052';
/** The refusal in survival mode; the only one that may offer "Switch anyway". */
export const CODE_WORKLOAD_SURVIVAL = 'LAI-LLM-053';

export interface WorkloadSwitcherProps {
  transport: WorkloadTransport;
  /** Offer "Switch anyway" (force) after a survival-mode refusal. Default false: survival keeps its brain. */
  allowForce?: boolean;
  /** Called after a workload became active. */
  onActivated?: (name: string) => void;
  /** Heading level of the title (default 2), so the host keeps its outline. */
  headingLevel?: 2 | 3 | 4;
  /** Only show these workloads (for example `['music']` on the music page). Default: all. */
  only?: string[];
  className?: string;
}

/** The label of a workload in a language (English when the Spanish one is empty). */
export function workloadLabel(w: Pick<Workload, 'name' | 'label'>, lang: Lang): string {
  return (lang === 'es' ? w.label?.es : w.label?.en) || w.label?.en || w.name;
}

type Busy = { name: string; progress: WorkloadProgress | null };

/**
 * WorkloadSwitcher: state first ("Now: Coding"), then one button per workload. A switch shows its progress; a failure
 * shows the doctor code and the one-sentence fix (ADR-0009). Buttons are never hidden: one that cannot work says why.
 */
export function WorkloadSwitcher({ transport, allowForce = false, onActivated, headingLevel = 2, only, className }: WorkloadSwitcherProps) {
  const t = useStrings(WORKLOAD_STRINGS);
  const lang = useLang();
  const titleId = useId();
  const [data, setData] = useState<WorkloadsResponse | null>(null);
  const [listError, setListError] = useState<TransportErrorInfo | null>(null);
  const [busy, setBusy] = useState<Busy | null>(null);
  const [failure, setFailure] = useState<{ name: string; error: TransportErrorInfo } | null>(null);
  const [doneName, setDoneName] = useState<string | null>(null);
  const abort = useRef<AbortController | null>(null);

  const load = useCallback(async () => {
    try {
      const d = await transport.list();
      setData(d);
      setListError(null);
    } catch (e) {
      setListError(toErrorInfo(e, 'LAI-SVC-116'));
    }
  }, [transport]);

  useEffect(() => {
    void load();
    return () => abort.current?.abort();
  }, [load]);

  const activate = async (w: Workload, force = false) => {
    abort.current?.abort();
    const ctl = new AbortController();
    abort.current = ctl;
    setFailure(null);
    setDoneName(null);
    setBusy({ name: w.name, progress: null });
    try {
      await transport.activate(w.name, {
        force,
        signal: ctl.signal,
        onProgress: (p) => {
          if (!ctl.signal.aborted) setBusy({ name: w.name, progress: p });
        },
      });
      if (ctl.signal.aborted) return;
      setDoneName(w.name);
      onActivated?.(w.name);
    } catch (e) {
      if (ctl.signal.aborted) return;
      setFailure({ name: w.name, error: toErrorInfo(e, CODE_WORKLOAD_FAILED) });
    } finally {
      if (!ctl.signal.aborted) {
        setBusy(null);
        void load();
      }
    }
  };

  const H = `h${headingLevel}` as 'h2';
  const all = data?.workloads ?? [];
  const shown = only ? all.filter((w) => only.includes(w.name)) : all;
  const active = data ? all.find((w) => w.name === data.active) : undefined;
  const failed = failure ? all.find((w) => w.name === failure.name) : undefined;
  const busyW = busy ? all.find((w) => w.name === busy.name) : undefined;

  return (
    <section className={`lai-workloads${className ? ` ${className}` : ''}`} aria-labelledby={titleId}>
      <H id={titleId} className="lai-workloads-title">
        {t('title')}
      </H>
      {!data && !listError ? <p className="lai-workloads-now">{t('loading')}</p> : null}
      {listError && !data ? (
        <>
          <p className="lai-workloads-now">{t('listFailed')}</p>
          <ErrorNotice error={listError} />
          <button type="button" className="lai-btn" onClick={() => void load()}>
            {t('retry')}
          </button>
        </>
      ) : null}
      {data ? (
        <>
          <p className="lai-workloads-now" data-testid="workload-now">
            {active ? t('now', { name: workloadLabel(active, lang) }) : t('nowNone')}
          </p>
          {!active && shown.length > 0 && !busy ? <p className="lai-workloads-next">{t('nextNone')}</p> : null}
          {shown.length === 0 ? <p className="lai-workloads-next">{t('empty')}</p> : null}
        </>
      ) : null}

      {busy ? (
        <div className="lai-workloads-progress" role="status">
          <p>{t('switching', { name: busyW ? workloadLabel(busyW, lang) : busy.name })}</p>
          {busy.progress?.progress != null ? (
            <progress max={1} value={busy.progress.progress} aria-label={t('switching', { name: busyW ? workloadLabel(busyW, lang) : busy.name })} />
          ) : (
            <progress aria-label={t('switching', { name: busyW ? workloadLabel(busyW, lang) : busy.name })} />
          )}
          {busy.progress?.step ? <p className="lai-hint">{t('switchingStep', { step: busy.progress.step })}</p> : null}
        </div>
      ) : null}

      {doneName && !busy ? (
        <p className="lai-workloads-done" role="status">
          {t('done', { name: workloadLabel(all.find((w) => w.name === doneName) ?? { name: doneName, label: { en: doneName, es: doneName } }, lang) })}
        </p>
      ) : null}

      {failure && !busy ? (
        <div className="lai-workloads-failure">
          <ErrorNotice error={failure.error} />
          {failed ? (
            <div className="lai-actions">
              {allowForce && failure.error.code === CODE_WORKLOAD_SURVIVAL ? (
                <button type="button" className="lai-btn" onClick={() => void activate(failed, true)}>
                  {t('force')}
                </button>
              ) : (
                <button type="button" className="lai-btn" onClick={() => void activate(failed)}>
                  {t('retry')}
                </button>
              )}
            </div>
          ) : null}
        </div>
      ) : null}

      {shown.length > 0 ? (
        <ul className="lai-workloads-list">
          {shown.map((w) => {
            const isActive = data?.active === w.name;
            const name = workloadLabel(w, lang);
            const noteId = `${titleId}-${w.name}-note`;
            const notes: string[] = [];
            if (isActive) notes.push(t('inUse'));
            if (!w.fits) notes.push(t('tooBig'));
            if (w.missing.length > 0) notes.push(t('missing', { models: w.missing.join(', ') }));
            return (
              <li key={w.name} className={`lai-workload${isActive ? ' lai-workload-active' : ''}`}>
                <button
                  type="button"
                  className={`lai-btn${isActive ? '' : ' lai-btn-primary'}`}
                  disabled={isActive || !w.fits || busy !== null}
                  aria-describedby={notes.length > 0 ? noteId : undefined}
                  data-workload={w.name}
                  onClick={() => void activate(w)}
                >
                  {isActive ? name : t('switchTo', { name })}
                </button>
                {notes.length > 0 ? (
                  <span id={noteId} className="lai-hint">
                    {notes.join(' · ')}
                  </span>
                ) : null}
              </li>
            );
          })}
        </ul>
      ) : null}
    </section>
  );
}
