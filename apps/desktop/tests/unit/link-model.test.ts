// What the link dialog shows (src/renderer/link/model.ts): the code, the countdown, every ending, both languages.
import { describe, expect, it } from 'vitest';
import { linkView } from '../../src/renderer/link/model';
import { STRINGS, clock, pick } from '../../src/renderer/link/strings';
import type { CloudLinkState, LinkBegin } from '../../src/shared/types';

const begin: LinkBegin = { userCode: 'ABCD-1234', verificationUri: 'https://ai.marin.cr/activate', expiresIn: 900 };
const cloud = (over: Partial<CloudLinkState> = {}): CloudLinkState => ({ linked: false, tokenHeld: false, url: 'https://ai.marin.cr', ...over });

describe('linkView', () => {
  it('before the code arrives: loading, no buttons', () => {
    expect(linkView({ begin: null, cloud: cloud(), secondsLeft: 0, lang: 'en' })).toMatchObject({ kind: 'loading', canOpen: false, canRetry: false });
  });

  it.each(['en', 'es'] as const)('the code, the instructions and the countdown in %s', (lang) => {
    const view = linkView({ begin, cloud: cloud({ link: { phase: 'pending' } }), secondsLeft: 872, lang });
    expect(view).toMatchObject({ kind: 'code', userCode: 'ABCD-1234', canOpen: true, canRetry: false, message: pick(STRINGS.instructions, lang) });
    expect(view.countdown).toContain('14:32');
  });

  it('the instructions are the spec texts', () => {
    expect(STRINGS.instructions.en).toBe('On any phone or computer, open ai.marin.cr/activate and enter this code.');
    expect(STRINGS.instructions.es).toBe('En cualquier teléfono o computadora, abre ai.marin.cr/activate e ingresa este código.');
  });

  it('the countdown reaching zero is the expired ending even before the main process says so', () => {
    expect(linkView({ begin, cloud: cloud({ link: { phase: 'pending' } }), secondsLeft: 0, lang: 'en' })).toMatchObject({ kind: 'error', canRetry: true, canOpen: false, message: STRINGS.expired.en });
  });

  it.each([
    ['expired', STRINGS.expired],
    ['denied', STRINGS.denied],
    ['unreachable', STRINGS.unreachable],
    ['cancelled', STRINGS.cancelled],
    ['failed', STRINGS.failed],
  ] as const)('the %s ending offers Try again and hides the code', (phase, text) => {
    const view = linkView({ begin, cloud: cloud({ link: { phase, ...(phase === 'expired' || phase === 'denied' ? { code: 'LAI-DSK-009' } : {}) } }), secondsLeft: 500, lang: 'es' });
    expect(view).toMatchObject({ kind: 'error', canRetry: true, canOpen: false, message: text.es });
    expect(view.userCode).toBeUndefined();
  });

  it('the expired and denied texts carry the doctor code', () => {
    expect(STRINGS.expired.en).toContain('LAI-DSK-009');
    expect(STRINGS.denied.es).toContain('LAI-DSK-009');
    expect(STRINGS.expired.en).toContain('The code expired. Try again.');
  });

  it.each([
    ['not_online', STRINGS.notOnline, true],
    ['already_linked', STRINGS.alreadyLinked, false],
    ['unreachable', STRINGS.unreachable, true],
    ['unavailable', STRINGS.unavailable, true],
  ] as const)('a refusal %s', (code, text, retry) => {
    expect(linkView({ begin: { ok: false, code }, cloud: cloud(), secondsLeft: 0, lang: 'en' })).toMatchObject({ kind: 'error', message: text.en, canRetry: retry });
    expect(STRINGS.notOnline.en).toBe('Switch this computer to Online first.');
  });

  it('saving, then connected (with the display name), and the memory-only note', () => {
    expect(linkView({ begin, cloud: cloud({ link: { phase: 'saving' } }), secondsLeft: 500, lang: 'en' })).toMatchObject({ kind: 'saving' });
    const done = linkView({ begin, cloud: cloud({ linked: true, tokenHeld: true, user: 'Person A', link: { phase: 'done' } }), secondsLeft: 500, lang: 'en' });
    expect(done).toMatchObject({ kind: 'done', message: 'Connected as Person A.', canOpen: false, canRetry: false });
    expect(linkView({ begin, cloud: cloud({ link: { phase: 'done' } }), secondsLeft: 500, lang: 'es' }).message).toBe('Conectado');
    const weak = linkView({ begin, cloud: cloud({ link: { phase: 'done', memoryOnly: true, code: 'LAI-DSK-008' } }), secondsLeft: 500, lang: 'en' });
    expect(weak.detail).toContain('LAI-DSK-008');
  });
});

describe('clock', () => {
  it.each([[872, '14:32'], [5, '0:05'], [0, '0:00'], [-3, '0:00'], [900, '15:00']])('%s s -> %s', (seconds, text) => {
    expect(clock(seconds)).toBe(text);
  });
});
