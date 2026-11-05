// What the link dialog shows for a given state (docs/specs/desktop.md 4.8). Pure: the page renders it, a unit test checks it.
import type { CloudLinkState, Lang, LinkBegin, LinkProgress, LinkRefusal } from '../../shared/types';
import { STRINGS, clock, fill, pick } from './strings';

export type LinkViewKind = 'loading' | 'code' | 'saving' | 'done' | 'error';

export interface LinkView {
  kind: LinkViewKind;
  /** The sentence under the title. */
  message: string;
  /** The code to show large: only while it can still be approved. */
  userCode?: string;
  /** "Open the browser" is offered. */
  canOpen: boolean;
  /** "Try again" is offered. */
  canRetry: boolean;
  /** "The code works for 14:32 more." */
  countdown?: string;
  /** A second line (the kind of storage, what the Lab tab does). */
  detail?: string;
}

export interface LinkInputs {
  /** The answer to `beginLink()`; null until it came. */
  begin: LinkBegin | LinkRefusal | null;
  /** `status.cloud` from the main process. */
  cloud: CloudLinkState | null;
  /** Whole seconds until the code expires, counted down by the page. */
  secondsLeft: number;
  lang: Lang;
}

function refusalMessage(refusal: LinkRefusal, lang: Lang): string {
  switch (refusal.code) {
    case 'not_online':
      return pick(STRINGS.notOnline, lang);
    case 'already_linked':
      return pick(STRINGS.alreadyLinked, lang);
    case 'unreachable':
      return pick(STRINGS.unreachable, lang);
    default:
      return pick(STRINGS.unavailable, lang);
  }
}

function endMessage(progress: LinkProgress, lang: Lang): string {
  switch (progress.phase) {
    case 'expired':
      return pick(STRINGS.expired, lang);
    case 'denied':
      return pick(STRINGS.denied, lang);
    case 'unreachable':
      return pick(STRINGS.unreachable, lang);
    case 'cancelled':
      return pick(STRINGS.cancelled, lang);
    default:
      return pick(STRINGS.failed, lang);
  }
}

export function linkView(input: LinkInputs): LinkView {
  const { begin, cloud, lang } = input;
  const progress = cloud?.link;
  if (progress?.phase === 'done') {
    const name = cloud?.user;
    return {
      kind: 'done',
      message: name ? fill(pick(STRINGS.connectedAs, lang), { name }) : pick(STRINGS.connected, lang),
      canOpen: false,
      canRetry: false,
      detail: progress.memoryOnly ? pick(STRINGS.memoryOnly, lang) : pick(STRINGS.connectedBody, lang),
    };
  }
  if (progress?.phase === 'saving') return { kind: 'saving', message: pick(STRINGS.saving, lang), canOpen: false, canRetry: false };
  if (begin === null) return { kind: 'loading', message: pick(STRINGS.loading, lang), canOpen: false, canRetry: false };
  if (!('userCode' in begin)) return { kind: 'error', message: refusalMessage(begin, lang), canOpen: false, canRetry: begin.code !== 'already_linked' };
  if (progress && progress.phase !== 'pending') return { kind: 'error', message: endMessage(progress, lang), canOpen: false, canRetry: true };
  if (input.secondsLeft <= 0) return { kind: 'error', message: pick(STRINGS.expired, lang), canOpen: false, canRetry: true };
  return {
    kind: 'code',
    message: pick(STRINGS.instructions, lang),
    userCode: begin.userCode,
    canOpen: true,
    canRetry: false,
    countdown: fill(pick(STRINGS.countdown, lang), { time: clock(input.secondsLeft) }),
    detail: pick(STRINGS.waiting, lang),
  };
}
