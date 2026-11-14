import { describe, expect, it, vi } from 'vitest';
import {
  INTERNET_OFF_MESSAGE,
  applyNavigation,
  decide,
  decideFrame,
  decidePermission,
  hardenedWebPreferences,
  installPermissionHandlers,
  installWebContentsHardening,
  registerView,
  rewriteAgentUrl,
  setNavigationHooks,
  viewOf,
  type Decision,
  type NavState,
  type View,
} from '../../src/main/navigation';

/** Written this way so the redaction scanner does not read a URL with credentials as an email address. */
const AT = String.fromCharCode(64);
const OFFLINE: NavState = { connected: false };
const CONNECTED: NavState = { connected: true };

describe('decide: content view (the agent UI)', () => {
  const rows: Array<[url: string, offline: Decision, connected: Decision]> = [
    ['http://127.0.0.1:7070/', 'allow', 'allow'],
    ['http://127.0.0.1:7070/portal', 'allow', 'allow'],
    ['http://127.0.0.1:7070/api/v1/status?x=1#top', 'allow', 'allow'],
    ['HTTP://127.0.0.1:7070/docs/', 'allow', 'allow'],
    // localhost:7070 is the same place; it is rewritten to 127.0.0.1 (see rewriteAgentUrl)
    ['http://localhost:7070/', 'allow', 'allow'],
    ['http://LOCALHOST:7070/settings', 'allow', 'allow'],
    // the three companion apps open their own window
    ['http://127.0.0.1:7070/chat/', 'open-child:chat', 'open-child:chat'],
    ['http://127.0.0.1:7070/chat', 'open-child:chat', 'open-child:chat'],
    ['http://127.0.0.1:7070/chat/props?x=1', 'open-child:chat', 'open-child:chat'],
    ['http://127.0.0.1:7070/library/', 'open-child:library', 'open-child:library'],
    ['http://127.0.0.1:7070/library/content/a/b', 'open-child:library', 'open-child:library'],
    ['http://127.0.0.1:7070/maps/', 'open-child:maps', 'open-child:maps'],
    ['http://localhost:7070/maps/', 'open-child:maps', 'open-child:maps'],
    ['http://127.0.0.1:7070/maps/catalog.json', 'open-child:maps', 'open-child:maps'],
    ['http://127.0.0.1:7070/music/', 'open-child:music', 'open-child:music'],
    ['http://127.0.0.1:7070/music', 'open-child:music', 'open-child:music'],
    ['http://localhost:7070/music/songs', 'open-child:music', 'open-child:music'],
    ['http://127.0.0.1:7070/musical', 'allow', 'allow'],
    // prefix is on a segment boundary and case-sensitive
    ['http://127.0.0.1:7070/chatter', 'allow', 'allow'],
    ['http://127.0.0.1:7070/Chat/', 'allow', 'allow'],
    // dot segments are resolved before any prefix test
    ['http://127.0.0.1:7070/chat/../api/v1/status', 'allow', 'allow'],
    ['http://127.0.0.1:7070/chat/%2e%2e/library/', 'open-child:library', 'open-child:library'],
    ['http://127.0.0.1:7070/docs/../chat/', 'open-child:chat', 'open-child:chat'],
    ['http://127.0.0.1:7070/%2e%2e/%2e%2e/etc/passwd', 'allow', 'allow'],
    // the cloud: only connected, only https, only the exact host on 443
    ['https://ai.marin.cr/', 'block', 'open-external'],
    ['https://ai.marin.cr/lab/nodes/ai-n1', 'block', 'open-external'],
    ['https://AI.MARIN.CR/x', 'block', 'open-external'],
    ['https://ai.marin.cr:443/x', 'block', 'open-external'],
    ['https://ai.marin.cr./x', 'block', 'open-external'],
    ['http://ai.marin.cr/', 'block', 'block'],
    ['https://ai.marin.cr:8443/', 'block', 'block'],
    ['https://ai.marin.cr:80/', 'block', 'block'],
    ['https://ai.marin.cr.evil.example/', 'block', 'block'],
    ['https://evil.example/ai.marin.cr', 'block', 'block'],
    [`https://user:pw${AT}ai.marin.cr/`, 'block', 'block'],
    [`https://ai.marin.cr${AT}evil.example/`, 'block', 'block'],
    // everything else
    ['https://example.com/', 'block', 'block'],
    ['http://example.com/', 'block', 'block'],
    ['javascript:alert(1)', 'block', 'block'],
    ['JavaScript:alert(1)', 'block', 'block'],
    ['file:///etc/passwd', 'block', 'block'],
    ['file://127.0.0.1:7070/', 'block', 'block'],
    ['data:text/html,<script>alert(1)</script>', 'block', 'block'],
    ['blob:http://127.0.0.1:7070/6f1c', 'block', 'block'],
    ['localai://app/topbar/', 'block', 'block'],
    ['ftp://127.0.0.1:7070/', 'block', 'block'],
    ['http://127.0.0.1:7071/', 'block', 'block'],
    ['http://127.0.0.1/', 'block', 'block'],
    ['http://127.0.0.2:7070/', 'block', 'block'],
    ['http://[::1]:7070/', 'block', 'block'],
    ['http://[::ffff:127.0.0.1]:7070/', 'block', 'block'],
    [`http://user:pw${AT}127.0.0.1:7070/`, 'block', 'block'],
    [`http://127.0.0.1:7070${AT}evil.example/`, 'block', 'block'],
    ['https://127.0.0.1:7070/', 'block', 'block'],
    ['', 'block', 'block'],
    ['not a url', 'block', 'block'],
    ['//example.com/', 'block', 'block'],
  ];
  it.each(rows)('%s', (url, offline, connected) => {
    expect(decide('content', url, OFFLINE)).toBe(offline);
    expect(decide('content', url, CONNECTED)).toBe(connected);
  });
});

describe('decide: child windows', () => {
  const rows: Array<[view: View, url: string, expected: Decision]> = [
    ['chat', 'http://127.0.0.1:7070/chat/', 'allow'],
    ['chat', 'http://127.0.0.1:7070/chat', 'allow'],
    ['chat', 'http://127.0.0.1:7070/chat/props', 'allow'],
    ['chat', 'http://localhost:7070/chat/', 'allow'],
    ['chat', 'http://127.0.0.1:7070/api/v1/models', 'allow'],
    ['chat', 'http://127.0.0.1:7070/assets/app.js', 'allow'],
    ['chat', 'http://127.0.0.1:7070/portal/assets/app.js', 'allow'],
    ['chat', 'http://127.0.0.1:7070/library/', 'block'],
    ['chat', 'http://127.0.0.1:7070/maps/', 'block'],
    ['chat', 'http://127.0.0.1:7070/', 'block'],
    ['chat', 'http://127.0.0.1:7070/chatter', 'block'],
    ['chat', 'http://127.0.0.1:7070/api/v2/x', 'block'],
    ['chat', 'http://127.0.0.1:7070/api/v1', 'block'],
    ['chat', 'http://127.0.0.1:7070/chat/../api/v1/models', 'allow'],
    ['chat', 'http://127.0.0.1:7070/chat/../library/', 'block'],
    ['chat', 'http://127.0.0.1:7070/chat/../api', 'block'],
    ['chat', 'http://127.0.0.1:7070/chat/%2e%2e/%2e%2e/etc', 'block'],
    ['chat', 'http://127.0.0.1:7070/chat%2f..%2fapi', 'block'],
    ['library', 'http://127.0.0.1:7070/library/', 'allow'],
    ['library', 'http://127.0.0.1:7070/library/content/wikipedia/A/Water', 'allow'],
    ['library', 'http://127.0.0.1:7070/chat/', 'block'],
    ['library', 'https://en.wikipedia.example/wiki/Water', 'block'],
    ['library', 'https://ai.marin.cr/', 'block'],
    ['maps', 'http://127.0.0.1:7070/maps/', 'allow'],
    ['maps', 'http://127.0.0.1:7070/maps/tiles/x.pmtiles', 'allow'],
    ['maps', 'http://127.0.0.1:7070/maps/assets/a.js', 'allow'],
    ['maps', 'http://127.0.0.1:7070/library/', 'block'],
    ['maps', 'http://[::1]:7070/maps/', 'block'],
    // Music (issue #515): confined to /music/ exactly as Chat is to /chat/
    ['music', 'http://127.0.0.1:7070/music/', 'allow'],
    ['music', 'http://127.0.0.1:7070/music', 'allow'],
    ['music', 'http://127.0.0.1:7070/music/songs/abc', 'allow'],
    ['music', 'http://localhost:7070/music/', 'allow'],
    ['music', 'http://127.0.0.1:7070/api/v1/music/songs/abc/audio', 'allow'],
    ['music', 'http://127.0.0.1:7070/api/v1/music/generate', 'allow'],
    ['music', 'http://127.0.0.1:7070/assets/app.js', 'allow'],
    ['music', 'http://127.0.0.1:7070/', 'block'],
    ['music', 'http://127.0.0.1:7070/chat/', 'block'],
    ['music', 'http://127.0.0.1:7070/library/', 'block'],
    ['music', 'http://127.0.0.1:7070/portal', 'block'],
    ['music', 'http://127.0.0.1:7070/musicx', 'block'],
    ['music', 'http://127.0.0.1:7070/Music/', 'block'],
    ['music', 'http://127.0.0.1:7070/music/../chat/', 'block'],
    ['music', 'http://127.0.0.1:7070/music/%2e%2e/settings', 'block'],
    ['music', 'http://127.0.0.1:7070/music%2f..%2fsettings', 'block'],
    ['music', 'http://127.0.0.1:8082/v1/tasks', 'block'],
    ['music', 'http://[::1]:7070/music/', 'block'],
    ['music', 'https://ai.marin.cr/music', 'block'],
    ['music', 'https://example.com/song.wav', 'block'],
    ['music', 'blob:http://127.0.0.1:7070/6f1c', 'block'],
    ['music', 'file:///home/person/song.wav', 'block'],
    ['music', 'javascript:alert(1)', 'block'],
    ['chat', 'http://127.0.0.1:7070/music/', 'block'],
    ['chat', 'javascript:alert(1)', 'block'],
    ['chat', 'file:///etc/passwd', 'block'],
    ['chat', 'data:text/html,hi', 'block'],
    ['chat', 'localai://app/topbar/', 'block'],
  ];
  it.each(rows)('%s: %s', (view, url, expected) => {
    expect(decide(view, url, OFFLINE)).toBe(expected);
    // child windows never open the cloud, connected or not
    expect(decide(view, url, CONNECTED)).toBe(expected);
  });
});

describe('decide: shell pages (top bar, Lab, error, PIN)', () => {
  const rows: Array<[url: string, expected: Decision]> = [
    ['localai://app/topbar/', 'allow'],
    ['localai://app/error/?code=LAI-DSK-004', 'allow'],
    ['localai://app/lab/', 'allow'],
    ['localai://app/pin/#x', 'allow'],
    ['localai://other/topbar/', 'block'],
    [`localai://user${AT}app/topbar/`, 'block'],
    ['http://127.0.0.1:7070/', 'block'],
    ['https://ai.marin.cr/', 'block'],
    ['https://example.com/', 'block'],
    ['javascript:alert(1)', 'block'],
    ['file:///etc/passwd', 'block'],
    ['data:text/html,hi', 'block'],
    ['devtools://devtools/bundled/inspector.html', 'block'],
    ['', 'block'],
  ];
  it.each(rows)('%s', (url, expected) => {
    expect(decide('shell', url, OFFLINE)).toBe(expected);
    expect(decide('shell', url, CONNECTED)).toBe(expected);
  });

  it('allows devtools:// and the dev server only in an unpackaged build', () => {
    const dev: NavState = { connected: false, dev: true, devOrigin: 'http://localhost:5173' };
    expect(decide('shell', 'devtools://devtools/bundled/inspector.html', dev)).toBe('allow');
    expect(decide('shell', 'http://localhost:5173/topbar/index.html', dev)).toBe('allow');
    expect(decide('shell', 'http://localhost:5174/', dev)).toBe('block');
    expect(decide('shell', 'http://evil.example:5173/', dev)).toBe('block');
    expect(decide('shell', 'http://localhost:5173/', OFFLINE)).toBe('block');
    expect(decide('content', 'http://localhost:5173/', dev)).toBe('block');
  });
});

describe('rewriteAgentUrl', () => {
  it.each([
    ['http://localhost:7070/chat/?a=1#b', 'http://127.0.0.1:7070/chat/?a=1#b'],
    ['http://LOCALHOST:7070', 'http://127.0.0.1:7070/'],
    ['http://127.0.0.1:7070/x', 'http://127.0.0.1:7070/x'],
    ['http://localhost:7070/chat/../api', 'http://127.0.0.1:7070/api'],
    ['http://localhost:8080/', null],
    ['https://localhost:7070/', null],
    ['http://example.com:7070/', null],
    [`http://user${AT}localhost:7070/`, null],
    ['nonsense', null],
  ])('%s', (input, expected) => {
    expect(rewriteAgentUrl(input)).toBe(expected);
  });
});

describe('decideFrame', () => {
  it('lets a frame move inside the agent origin and blocks everything else', () => {
    expect(decideFrame('content', 'http://127.0.0.1:7070/docs/', OFFLINE)).toBe('allow');
    expect(decideFrame('content', 'http://127.0.0.1:7070/library/', OFFLINE)).toBe('allow');
    expect(decideFrame('content', 'https://example.com/', OFFLINE)).toBe('block');
    expect(decideFrame('content', 'https://ai.marin.cr/', CONNECTED)).toBe('block');
    expect(decideFrame('library', 'http://127.0.0.1:7070/library/content/x', OFFLINE)).toBe('allow');
    expect(decideFrame('library', 'https://example.com/', OFFLINE)).toBe('block');
    expect(decideFrame('shell', 'http://127.0.0.1:7070/', OFFLINE)).toBe('block');
  });
});

describe('the messages for a blocked link', () => {
  it('are the two of the spec', () => {
    expect(INTERNET_OFF_MESSAGE.en).toBe('Links to the internet are turned off on this computer.');
    expect(INTERNET_OFF_MESSAGE.es).toBe('Los enlaces a internet están desactivados en esta computadora.');
  });
});

describe('decidePermission', () => {
  const agent = 'http://127.0.0.1:7070/portal';
  const rows: Array<[string, Parameters<typeof decidePermission>[0], boolean]> = [
    ['clipboard write, shell page', { permission: 'clipboard-sanitized-write', requestingUrl: 'localai://app/topbar/' }, true],
    ['clipboard write, agent page', { permission: 'clipboard-sanitized-write', requestingUrl: agent }, false],
    ['clipboard write, internet page', { permission: 'clipboard-sanitized-write', requestingUrl: 'https://example.com/' }, false],
    ['clipboard read, shell page', { permission: 'clipboard-read', requestingUrl: 'localai://app/topbar/' }, false],
    ['microphone, agent page', { permission: 'media', requestingUrl: agent, mediaTypes: ['audio'] }, true],
    ['camera, agent page', { permission: 'media', requestingUrl: agent, mediaTypes: ['video'] }, false],
    ['camera and microphone, agent page', { permission: 'media', requestingUrl: agent, mediaTypes: ['audio', 'video'] }, false],
    ['media without types', { permission: 'media', requestingUrl: agent }, false],
    ['microphone, shell page', { permission: 'media', requestingUrl: 'localai://app/lab/', mediaTypes: ['audio'] }, false],
    ['microphone, other port', { permission: 'media', requestingUrl: 'http://127.0.0.1:8080/', mediaTypes: ['audio'] }, false],
    ['microphone, internet page', { permission: 'media', requestingUrl: 'https://example.com/', mediaTypes: ['audio'] }, false],
    // Music (issue #515): never the microphone, by URL or by the window it is asked from
    ['microphone, music page', { permission: 'media', requestingUrl: 'http://127.0.0.1:7070/music/', mediaTypes: ['audio'] }, false],
    ['microphone, music page without slash', { permission: 'media', requestingUrl: 'http://127.0.0.1:7070/music', mediaTypes: ['audio'] }, false],
    ['microphone, music window at the origin', { permission: 'media', requestingUrl: 'http://127.0.0.1:7070/', mediaTypes: ['audio'], view: 'music' }, false],
    ['microphone, chat window', { permission: 'media', requestingUrl: 'http://127.0.0.1:7070/chat/', mediaTypes: ['audio'], view: 'chat' }, true],
    ['microphone, /musical is not music', { permission: 'media', requestingUrl: 'http://127.0.0.1:7070/musical', mediaTypes: ['audio'] }, true],
    ['clipboard write, music page', { permission: 'clipboard-sanitized-write', requestingUrl: 'http://127.0.0.1:7070/music/', view: 'music' }, false],
    ['file system, music page', { permission: 'fileSystem', requestingUrl: 'http://127.0.0.1:7070/music/', view: 'music' }, false],
    ['notifications', { permission: 'notifications', requestingUrl: agent }, false],
    ['geolocation', { permission: 'geolocation', requestingUrl: agent }, false],
    ['openExternal', { permission: 'openExternal', requestingUrl: agent }, false],
    ['display capture', { permission: 'display-capture', requestingUrl: agent }, false],
    ['midi', { permission: 'midi', requestingUrl: agent }, false],
    ['bad url', { permission: 'media', requestingUrl: 'nonsense', mediaTypes: ['audio'] }, false],
    ['empty url', { permission: 'clipboard-sanitized-write', requestingUrl: '' }, false],
  ];
  it.each(rows)('%s', (_name, query, expected) => {
    expect(decidePermission(query)).toBe(expected);
  });

  it('the dev server may write the clipboard only in an unpackaged build', () => {
    const query = { permission: 'clipboard-sanitized-write', requestingUrl: 'http://localhost:5173/topbar/index.html', devOrigin: 'http://localhost:5173' };
    expect(decidePermission(query)).toBe(false);
    expect(decidePermission({ ...query, dev: true })).toBe(true);
  });

  it('installPermissionHandlers denies by default on the request, check and device handlers', () => {
    let request: ((c: unknown, p: string, cb: (g: boolean) => void, d: unknown) => void) | undefined;
    let check: ((c: unknown, p: string, o: string, d: unknown) => boolean) | undefined;
    let device: (() => boolean) | undefined;
    installPermissionHandlers({
      setPermissionRequestHandler: (h: never) => (request = h),
      setPermissionCheckHandler: (h: never) => (check = h),
      setDevicePermissionHandler: (h: never) => (device = h),
    });
    const ask = (permission: string, details: object): boolean => {
      let granted: boolean | undefined;
      request!(null, permission, (g) => (granted = g), details);
      return granted!;
    };
    expect(ask('media', { requestingUrl: 'http://127.0.0.1:7070/portal', mediaTypes: ['audio'] })).toBe(true);
    expect(ask('media', { requestingUrl: 'http://127.0.0.1:7070/portal', mediaTypes: ['audio', 'video'] })).toBe(false);
    expect(ask('clipboard-sanitized-write', { requestingUrl: 'localai://app/topbar/' })).toBe(true);
    expect(ask('notifications', { requestingUrl: 'localai://app/topbar/' })).toBe(false);
    expect(ask('geolocation', {})).toBe(false);
    expect(check!(null, 'media', 'http://127.0.0.1:7070', { requestingUrl: 'http://127.0.0.1:7070/portal', mediaType: 'audio' })).toBe(true);
    expect(check!(null, 'media', 'http://127.0.0.1:7070', { requestingUrl: 'http://127.0.0.1:7070/portal', mediaType: 'video' })).toBe(false);
    expect(check!(null, 'clipboard-sanitized-write', 'localai://app', { requestingUrl: 'localai://app/topbar/' })).toBe(true);
    expect(check!(null, 'notifications', 'localai://app', {})).toBe(false);
    expect(device!()).toBe(false);
  });

  it('the Music window gets no microphone even when the frame URL alone would pass (issue #515)', () => {
    let request: ((c: unknown, p: string, cb: (g: boolean) => void, d: unknown) => void) | undefined;
    let check: ((c: unknown, p: string, o: string, d: unknown) => boolean) | undefined;
    installPermissionHandlers({
      setPermissionRequestHandler: (h: never) => (request = h),
      setPermissionCheckHandler: (h: never) => (check = h),
      setDevicePermissionHandler: () => undefined,
    });
    const musicWindow = {};
    const chatWindow = {};
    registerView(musicWindow as never, 'music');
    registerView(chatWindow as never, 'chat');
    const ask = (contents: object): boolean => {
      let granted: boolean | undefined;
      request!(contents, 'media', (g) => (granted = g), { requestingUrl: 'http://127.0.0.1:7070/', mediaTypes: ['audio'] });
      return granted!;
    };
    expect(ask(musicWindow)).toBe(false);
    expect(ask(chatWindow)).toBe(true);
    expect(check!(musicWindow, 'media', 'http://127.0.0.1:7070', { mediaType: 'audio' })).toBe(false);
    expect(check!(chatWindow, 'media', 'http://127.0.0.1:7070', { mediaType: 'audio' })).toBe(true);
  });
});

describe('hardenedWebPreferences', () => {
  it('is the section 3.4 set, with devTools only when unpackaged', () => {
    expect(hardenedWebPreferences(true)).toEqual({
      contextIsolation: true,
      sandbox: true,
      nodeIntegration: false,
      webSecurity: true,
      spellcheck: false,
      devTools: false,
    });
    expect(hardenedWebPreferences(false).devTools).toBe(true);
  });
});

describe('applyNavigation and the web-contents hooks', () => {
  function fakeContents() {
    const handlers: Record<string, (...args: never[]) => unknown> = {};
    let openHandler: ((d: { url: string }) => { action: string }) | undefined;
    const loaded: string[] = [];
    const contents = {
      session: { setSpellCheckerEnabled: vi.fn() },
      setWindowOpenHandler: (h: typeof openHandler) => (openHandler = h),
      on: (name: string, h: (...args: never[]) => unknown) => (handlers[name] = h),
      loadURL: (u: string) => (loaded.push(u), Promise.resolve()),
    };
    return { contents, handlers, loaded, open: (url: string) => openHandler!({ url }) };
  }
  function boot(state: NavState = OFFLINE) {
    let created: ((e: unknown, c: unknown) => void) | undefined;
    installWebContentsHardening({ on: (_n: string, h: never) => (created = h) } as never, { getState: () => state });
    const fake = fakeContents();
    created!({}, fake.contents);
    return fake;
  }
  const hooks = { openChild: vi.fn(), openExternal: vi.fn(), contentBlocked: vi.fn() };
  setNavigationHooks(hooks);
  const navigate = (fake: ReturnType<typeof boot>, url: string, name = 'will-navigate') => {
    const event = { preventDefault: vi.fn(), url, isMainFrame: true };
    (fake.handlers[name] as unknown as (e: unknown, u: string) => void)(event, url);
    return event;
  };

  it('every window.open is denied, and is routed first', () => {
    const fake = boot();
    registerView(fake.contents as never, 'content');
    hooks.openChild.mockClear();
    expect(fake.open('http://127.0.0.1:7070/maps/')).toEqual({ action: 'deny' });
    expect(hooks.openChild).toHaveBeenCalledWith('maps');
    expect(fake.open('https://example.com/')).toEqual({ action: 'deny' });
    expect(hooks.contentBlocked).toHaveBeenCalled();
    expect(fake.open('http://127.0.0.1:7070/docs/')).toEqual({ action: 'deny' });
    expect(fake.loaded).toEqual(['http://127.0.0.1:7070/docs/']);
  });

  it('a content-view link to the cloud opens the browser only when connected, with the normalised URL', () => {
    const offline = boot(OFFLINE);
    registerView(offline.contents as never, 'content');
    hooks.openExternal.mockClear();
    const blocked = navigate(offline, 'https://ai.marin.cr/lab');
    expect(blocked.preventDefault).toHaveBeenCalled();
    expect(hooks.openExternal).not.toHaveBeenCalled();

    const connected = boot(CONNECTED);
    registerView(connected.contents as never, 'content');
    const event = navigate(connected, 'https://AI.marin.cr/lab');
    expect(event.preventDefault).toHaveBeenCalled();
    expect(hooks.openExternal).toHaveBeenCalledWith('https://ai.marin.cr/lab');
  });

  it('will-navigate: allowed places pass; localhost is rewritten; a child link opens the child window; the rest is cancelled', () => {
    const fake = boot();
    registerView(fake.contents as never, 'content');
    expect(navigate(fake, 'http://127.0.0.1:7070/docs/').preventDefault).not.toHaveBeenCalled();
    const rewritten = navigate(fake, 'http://localhost:7070/docs/');
    expect(rewritten.preventDefault).toHaveBeenCalled();
    expect(fake.loaded.at(-1)).toBe('http://127.0.0.1:7070/docs/');
    hooks.openChild.mockClear();
    expect(navigate(fake, 'http://127.0.0.1:7070/library/').preventDefault).toHaveBeenCalled();
    expect(hooks.openChild).toHaveBeenCalledWith('library');
    for (const url of ['https://example.com/', 'javascript:alert(1)', 'file:///etc/passwd', 'data:text/html,x']) {
      expect(navigate(fake, url).preventDefault, url).toHaveBeenCalled();
    }
  });

  it('will-redirect never opens a window and cancels anything but an allowed place', () => {
    const fake = boot();
    registerView(fake.contents as never, 'content');
    hooks.openChild.mockClear();
    hooks.openExternal.mockClear();
    expect(navigate(fake, 'http://127.0.0.1:7070/docs/', 'will-redirect').preventDefault).not.toHaveBeenCalled();
    expect(navigate(fake, 'https://example.com/', 'will-redirect').preventDefault).toHaveBeenCalled();
    expect(navigate(fake, 'http://127.0.0.1:7070/chat/', 'will-redirect').preventDefault).toHaveBeenCalled();
    expect(hooks.openChild).not.toHaveBeenCalled();
    const connected = boot(CONNECTED);
    registerView(connected.contents as never, 'content');
    expect(navigate(connected, 'https://ai.marin.cr/', 'will-redirect').preventDefault).toHaveBeenCalled();
    expect(hooks.openExternal).not.toHaveBeenCalled();
  });

  it('a child window blocks silently', () => {
    const fake = boot();
    registerView(fake.contents as never, 'library');
    hooks.contentBlocked.mockClear();
    expect(navigate(fake, 'https://en.wikipedia.example/wiki/Water').preventDefault).toHaveBeenCalled();
    expect(navigate(fake, 'http://127.0.0.1:7070/library/content/x').preventDefault).not.toHaveBeenCalled();
    expect(hooks.contentBlocked).not.toHaveBeenCalled();
  });

  it('an unregistered webContents is a shell page: only localai://app', () => {
    const fake = boot();
    expect(viewOf(fake.contents as never)).toBe('shell');
    expect(navigate(fake, 'http://127.0.0.1:7070/').preventDefault).toHaveBeenCalled();
    expect(navigate(fake, 'localai://app/error/').preventDefault).not.toHaveBeenCalled();
  });

  it('subframes follow decideFrame; the main frame is left to will-navigate', () => {
    const fake = boot();
    registerView(fake.contents as never, 'library');
    const frame = (url: string, isMainFrame: boolean) => {
      const event = { preventDefault: vi.fn(), url, isMainFrame };
      (fake.handlers['will-frame-navigate'] as unknown as (e: unknown) => void)(event);
      return event;
    };
    expect(frame('https://example.com/', false).preventDefault).toHaveBeenCalled();
    expect(frame('http://127.0.0.1:7070/library/x', false).preventDefault).not.toHaveBeenCalled();
    expect(frame('https://example.com/', true).preventDefault).not.toHaveBeenCalled();
  });

  it('refuses <webview> and turns spell checking off', () => {
    const fake = boot();
    const event = { preventDefault: vi.fn() };
    (fake.handlers['will-attach-webview'] as unknown as (e: unknown) => void)(event);
    expect(event.preventDefault).toHaveBeenCalled();
    expect(fake.contents.session.setSpellCheckerEnabled).toHaveBeenCalledWith(false);
  });

  it('applyNavigation: shell pages are never rewritten', () => {
    const fake = fakeContents();
    expect(applyNavigation(fake.contents as never, 'shell', 'localai://app/pin/', OFFLINE, 'navigate')).toEqual({ prevent: false });
  });
});
