import { describe, expect, it } from 'vitest';
import { NOTIFY_WINDOW_MS, Notifier, RateLimiter, describeEvent, describeSuggestion, type NotificationPort } from '../../src/main/notify';
import { NOTICE_COPY, NOTIFY_TEXT, fill, type Lang } from '../../src/main/i18n';

interface Shown {
  title: string;
  body: string;
}

function setup(over: { lang?: Lang; supported?: boolean } = {}) {
  let now = 1_000_000;
  let lang: Lang = over.lang ?? 'en';
  const shown: Shown[] = [];
  const clicks: Array<() => void> = [];
  const port: NotificationPort = {
    supported: () => over.supported ?? true,
    show: (n, onClick) => {
      shown.push(n);
      clicks.push(onClick);
    },
  };
  let activated = 0;
  const notifier = new Notifier({ port, lang: () => lang, clock: () => now, onActivate: () => void (activated += 1) });
  return {
    notifier,
    shown,
    clicks,
    activated: () => activated,
    advance: (ms: number) => void (now += ms),
    setLang: (next: Lang) => void (lang = next),
  };
}

const alert = (severity: string, extra: Record<string, unknown> = {}) => ({ type: 'alert.raised', data: { code: 'LAI-HW-012', severity, title: 'Running on battery', fix: 'Plug in the charger.', ...extra } });

describe('RateLimiter', () => {
  it('allows one per kind per window, with a fake clock', () => {
    let now = 0;
    const limiter = new RateLimiter(NOTIFY_WINDOW_MS, () => now);
    expect(limiter.allow('a')).toBe(true);
    expect(limiter.allow('a')).toBe(false);
    expect(limiter.allow('b')).toBe(true);
    now = NOTIFY_WINDOW_MS - 1;
    expect(limiter.allow('a')).toBe(false);
    now = NOTIFY_WINDOW_MS;
    expect(limiter.allow('a')).toBe(true);
    expect(limiter.allow('a')).toBe(false);
  });

  it('a suppressed attempt does not extend the window', () => {
    let now = 0;
    const limiter = new RateLimiter(1000, () => now);
    expect(limiter.allow('a')).toBe(true);
    now = 900;
    expect(limiter.allow('a')).toBe(false);
    now = 1000;
    expect(limiter.allow('a')).toBe(true);
  });

  it('the window is 5 minutes', () => {
    expect(NOTIFY_WINDOW_MS).toBe(300_000);
  });
});

describe('Notifier rate limit', () => {
  it('a burst of 10 alert.raised within 5 minutes produces one notification', () => {
    const t = setup();
    for (let i = 0; i < 10; i += 1) {
      expect(t.notifier.notifyEvent(alert('warning'))).toBe(i === 0);
      t.advance(20_000);
    }
    expect(t.shown).toHaveLength(1);
    // 5 minutes after the first one (not after the tenth) the next is allowed.
    t.advance(NOTIFY_WINDOW_MS);
    expect(t.notifier.notifyEvent(alert('warning'))).toBe(true);
    expect(t.shown).toHaveLength(2);
  });

  it('different kinds do not limit each other', () => {
    const t = setup();
    t.notifier.notifyEvent(alert('critical'));
    t.notifier.notifyEvent({ type: 'drive.mounted', data: { root_id: 'r', mount: '/m', state: 'ready', read_only: false } });
    t.notifier.notifyEvent({ type: 'model.failed', data: { model: 'model-a', code: 'LAI-LLM-001' } });
    t.notifier.suggestSurvival();
    expect(t.shown).toHaveLength(4);
  });

  it('a switch to Survival right after a switch to Offline is not swallowed, a second Offline is', () => {
    const t = setup();
    expect(t.notifier.notifyEvent({ type: 'mode.changed', data: { from: 'online', to: 'offline' } })).toBe(true);
    expect(t.notifier.notifyEvent({ type: 'mode.changed', data: { from: 'offline', to: 'survival' } })).toBe(true);
    expect(t.notifier.notifyEvent({ type: 'mode.changed', data: { from: 'survival', to: 'offline' } })).toBe(false);
  });

  it('a failed sync is not hidden by a sync that just finished', () => {
    const t = setup();
    expect(t.notifier.notifyEvent({ type: 'sync.done', data: { state: 'done', totals: {} } })).toBe(true);
    expect(t.notifier.notifyEvent({ type: 'sync.done', data: { state: 'failed', totals: {} } })).toBe(true);
  });

  it('a click on a notification activates the app', () => {
    const t = setup();
    t.notifier.notifyEvent(alert('warning'));
    t.clicks[0]!();
    expect(t.activated()).toBe(1);
  });

  it('a desktop without notifications shows nothing and does not throw', () => {
    const t = setup({ supported: false });
    expect(t.notifier.notifyEvent(alert('warning'))).toBe(false);
    expect(t.notifier.notice('LAI-DSK-013')).toBe(false);
    expect(t.shown).toHaveLength(0);
  });
});

describe('which events notify', () => {
  it('alerts: warning and above only', () => {
    expect(describeEvent(alert('info'), 'en')).toBeNull();
    expect(describeEvent(alert('warning'), 'en')).not.toBeNull();
    expect(describeEvent(alert('error'), 'en')).not.toBeNull();
    expect(describeEvent(alert('critical'), 'en')).not.toBeNull();
    expect(describeEvent(alert('bogus'), 'en')).toBeNull();
    expect(describeEvent({ type: 'alert.cleared', data: { code: 'LAI-HW-012', severity: 'warning' } }, 'en')).toBeNull();
  });

  it('shows the alert title, fix and code', () => {
    expect(describeEvent(alert('warning'), 'en')).toEqual({ kind: 'alert.raised', title: 'Local AI needs attention', body: 'Running on battery Plug in the charger. (LAI-HW-012)' });
  });

  it('mode changes name the new mode in the UI language', () => {
    expect(describeEvent({ type: 'mode.changed', data: { from: 'online', to: 'survival' } }, 'en')?.body).toBe('Local AI is now in survival mode.');
    expect(describeEvent({ type: 'mode.changed', data: { from: 'online', to: 'survival' } }, 'es')?.body).toBe('Local AI ahora está en modo supervivencia.');
    expect(describeEvent({ type: 'mode.changed', data: { from: 'online', to: 'offline' } }, 'es')?.body).toBe('Local AI ahora está en modo sin conexión.');
    expect(describeEvent({ type: 'mode.changed', data: { to: 'weird' } }, 'en')).toBeNull();
  });

  it('model failures name the model and the code', () => {
    expect(describeEvent({ type: 'model.failed', data: { model: 'model-a', code: 'LAI-LLM-001' } }, 'en')?.body).toContain('model-a');
    expect(describeEvent({ type: 'model.failed', data: { model: 'model-a', code: 'LAI-LLM-001' } }, 'en')?.body).toContain('LAI-LLM-001');
    expect(describeEvent({ type: 'model.failed', data: { model: 'model-a' } }, 'es')?.body).not.toContain('{');
  });

  it('sync: done, done with failures and failed notify; cancelled and skipped do not', () => {
    expect(describeEvent({ type: 'sync.done', data: { state: 'done', totals: { failed: 0 } } }, 'en')?.body).toBe('Everything is up to date.');
    expect(describeEvent({ type: 'sync.done', data: { state: 'done', totals: { failed: 3 } } }, 'en')?.body).toContain('3 items');
    expect(describeEvent({ type: 'sync.done', data: { state: 'failed' } }, 'en')?.title).toBe('Sync failed');
    expect(describeEvent({ type: 'sync.done', data: { state: 'cancelled' } }, 'en')).toBeNull();
    expect(describeEvent({ type: 'sync.done', data: { state: 'skipped' } }, 'en')).toBeNull();
  });

  it('drives: mounted, read only, unmounted safely and surprise removal', () => {
    expect(describeEvent({ type: 'drive.mounted', data: { read_only: false } }, 'en')?.body).toBe('The Local AI drive was plugged in.');
    expect(describeEvent({ type: 'drive.mounted', data: { read_only: true } }, 'en')?.body).toContain('read only');
    expect(describeEvent({ type: 'drive.unmounted', data: { safe_to_unplug: true } }, 'en')?.body).toContain('safely');
    expect(describeEvent({ type: 'drive.unmounted', data: { safe_to_unplug: false } }, 'en')?.body).toContain('without ejecting');
  });

  it('events that are not announced', () => {
    for (const type of ['model.loaded', 'model.loading', 'mode.step', 'service.state', 'hardware.power', 'sync.progress', 'resync', 'shutdown', 'cloud.state']) {
      expect(describeEvent({ type, data: {} }, 'en'), type).toBeNull();
    }
    expect(describeEvent({ type: 'mode.changed', data: null }, 'en')).toBeNull();
  });

  it('the suggestion is the sentence of modes.md, in both languages', () => {
    expect(describeSuggestion('en').body).toBe('No internet for 30 minutes. Everything here still works. Switch to Survival mode?');
    expect(describeSuggestion('es').body).toBe('Sin internet durante 30 minutos. Todo lo de aquí sigue funcionando. ¿Cambiar a modo supervivencia?');
  });
});

describe('language', () => {
  it('follows the UI language at the moment of the notification', () => {
    const t = setup({ lang: 'en' });
    t.notifier.notifyEvent({ type: 'drive.mounted', data: {} });
    t.advance(NOTIFY_WINDOW_MS);
    t.setLang('es');
    t.notifier.notifyEvent({ type: 'drive.mounted', data: {} });
    expect(t.shown.map((s) => s.title)).toEqual(['Drive connected', 'Unidad conectada']);
  });

  it('every text has a Spanish version that differs from the English one and leaves no placeholder open', () => {
    for (const [key, text] of Object.entries(NOTIFY_TEXT)) {
      expect(text.es, key).not.toBe(text.en);
      expect(text.es.length, key).toBeGreaterThan(5);
      // The placeholders of both languages are the same set.
      expect(text.es.match(/\{\w+\}/g)?.sort() ?? [], key).toEqual(text.en.match(/\{\w+\}/g)?.sort() ?? []);
    }
    expect(fill('{a} and {b}', { a: 'x' })).toBe('x and {b}');
  });
});

describe('LAI-DSK-013 notice', () => {
  it('names the code, says what to turn on, and is not rate limited', () => {
    const t = setup({ lang: 'en' });
    expect(t.notifier.notice('LAI-DSK-013')).toBe(true);
    expect(t.notifier.notice('LAI-DSK-013')).toBe(true);
    expect(t.shown[0]!.title).toBe("The system tray isn't available (LAI-DSK-013)");
    expect(t.shown[0]!.body).toContain('AppIndicator and KStatusNotifierItem Support');
    t.setLang('es');
    t.notifier.notice('LAI-DSK-013');
    expect(t.shown[2]!.title).toContain('bandeja');
    expect(NOTICE_COPY['LAI-DSK-013'].fix.en).toBe('Turn on "AppIndicator and KStatusNotifierItem Support" in Extensions, then restart ai-desktop.');
  });
});
