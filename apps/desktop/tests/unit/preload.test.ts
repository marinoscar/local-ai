// The two preload bridges (docs/specs/desktop.md 3.5): exactly the members of the spec, each on the channel of ipc.ts,
// and nothing from Node. The agent bridge must not grow: it has the five names and no more.
import { describe, expect, it, vi } from 'vitest';
import { CHANNELS } from '../../src/main/ipc';
import { type HostIpcLike, createHostBridge } from '../../src/preload/agent';
import { type PinIpcLike, createPinBridge } from '../../src/preload/pin';
import { type IpcRendererLike, createShellBridge } from '../../src/preload/shell';
import { HOST_MEMBERS } from '../../src/shared/types';

function fakeShellIpc() {
  const invoked: Array<[string, unknown[]]> = [];
  const sent: Array<[string, unknown[]]> = [];
  const listeners = new Map<string, Set<(event: unknown, ...args: unknown[]) => void>>();
  const ipc: IpcRendererLike = {
    invoke: vi.fn(async (channel: string, ...args: unknown[]) => void invoked.push([channel, args])),
    send: vi.fn((channel: string, ...args: unknown[]) => void sent.push([channel, args])),
    sendSync: vi.fn(() => 'es'),
    on: vi.fn((channel: string, listener: (event: unknown, ...args: unknown[]) => void) => {
      listeners.set(channel, (listeners.get(channel) ?? new Set()).add(listener));
    }),
    removeListener: vi.fn((channel: string, listener: (event: unknown, ...args: unknown[]) => void) => void listeners.get(channel)?.delete(listener)),
  };
  return { ipc, invoked, sent, listeners };
}

describe('window.localaiDesktop (shell pages)', () => {
  it('has the members of the spec interface plus the DSK-04 additions, and nothing else', () => {
    const bridge = createShellBridge(fakeShellIpc().ipc);
    expect(Object.keys(bridge).sort()).toEqual(
      [
        // desktop.md 3.5
        'status', 'onStatus', 'startAgent', 'copyApiKey', 'connectionInfo', 'revealKey', 'setMode', 'openWindow', 'showTab', 'cloud', 'settings', 'pinSubmit', 'lang',
        // DSK-04
        'errorAction', 'showMenu', 'onNotice',
        // DSK-08
        'rotateKey',
        // DSK-09 (survival strip)
        'setFullScreen', 'closePortal',
        // issue #480 (Install what's missing)
        'ensure',
      ].sort(),
    );
    expect(Object.keys(bridge.cloud).sort()).toEqual(['beginLink', 'cached', 'linkState', 'openNode', 'openVerification', 'request', 'unlink']);
    expect(Object.keys(bridge.settings).sort()).toEqual(['get', 'set']);
    expect(Object.keys(bridge.ensure).sort()).toEqual(['cancel', 'confirm', 'start', 'view']);
  });

  it('exposes no Node: no require, process, Buffer, ipcRenderer or fs on the object', () => {
    const bridge = createShellBridge(fakeShellIpc().ipc) as unknown as Record<string, unknown>;
    for (const name of ['require', 'process', 'Buffer', 'ipcRenderer', 'fs', 'shell', 'electron']) expect(bridge[name]).toBeUndefined();
  });

  it('every member calls its own channel with plain arguments', async () => {
    const { ipc, invoked, sent } = fakeShellIpc();
    const b = createShellBridge(ipc);
    await b.status();
    await b.startAgent();
    await b.copyApiKey();
    await b.connectionInfo();
    await b.revealKey();
    await b.setMode('offline');
    await b.cloud.beginLink();
    await b.cloud.linkState();
    await b.cloud.unlink();
    await b.cloud.request({ method: 'GET', path: '/api/localai/nodes' });
    await b.cloud.cached();
    await b.settings.get();
    await b.settings.set({ autostart: false });
    await b.pinSubmit('1234');
    await b.rotateKey();
    await b.errorAction('open-portal', 'LAI-DSK-004');
    await b.ensure.view();
    await b.ensure.start();
    await b.ensure.cancel();
    await b.ensure.confirm(['whisper.build']);
    b.openWindow('chat');
    b.showTab('lab');
    b.showMenu();
    b.setFullScreen(false);
    b.closePortal();
    b.cloud.openVerification();
    b.cloud.openNode('ai-n2');

    expect(invoked).toEqual([
      [CHANNELS.status, []],
      [CHANNELS.startAgent, []],
      [CHANNELS.copyApiKey, []],
      [CHANNELS.connectionInfo, []],
      [CHANNELS.revealKey, []],
      [CHANNELS.setMode, ['offline']],
      [CHANNELS.cloudBeginLink, []],
      [CHANNELS.cloudLinkState, []],
      [CHANNELS.cloudUnlink, []],
      [CHANNELS.cloudRequest, [{ method: 'GET', path: '/api/localai/nodes' }]],
      [CHANNELS.cloudCached, []],
      [CHANNELS.settingsGet, []],
      [CHANNELS.settingsSet, [{ autostart: false }]],
      [CHANNELS.pinSubmit, ['1234']],
      [CHANNELS.rotateKey, []],
      [CHANNELS.errorAction, [{ action: 'open-portal', code: 'LAI-DSK-004' }]],
      [CHANNELS.ensureView, []],
      [CHANNELS.ensureStart, []],
      [CHANNELS.ensureCancel, []],
      [CHANNELS.ensureConfirm, [['whisper.build']]],
    ]);
    expect(sent).toEqual([
      [CHANNELS.openWindow, ['chat']],
      [CHANNELS.showTab, ['lab']],
      [CHANNELS.showMenu, []],
      [CHANNELS.setFullScreen, [false]],
      [CHANNELS.closePortal, []],
      [CHANNELS.cloudOpenVerification, []],
      [CHANNELS.cloudOpenNode, ['ai-n2']],
    ]);
  });

  it('lang() is a synchronous call that answers en or es only', () => {
    const { ipc } = fakeShellIpc();
    expect(createShellBridge(ipc).lang()).toBe('es');
    (ipc.sendSync as ReturnType<typeof vi.fn>).mockReturnValue('<script>');
    expect(createShellBridge(ipc).lang()).toBe('en');
    expect(ipc.sendSync).toHaveBeenCalledWith(CHANNELS.lang);
  });

  it('onStatus and onNotice subscribe to the push channels, hand over the payload only and unsubscribe', () => {
    const { ipc, listeners } = fakeShellIpc();
    const b = createShellBridge(ipc);
    const onStatus = vi.fn();
    const onNotice = vi.fn();
    const offStatus = b.onStatus(onStatus);
    b.onNotice(onNotice);
    for (const l of listeners.get(CHANNELS.statusChanged)!) l({ sender: 'secret-event-object' }, { agent: 'up' });
    for (const l of listeners.get(CHANNELS.notice)!) l({}, { kind: 'internet-off', text: 'x' });
    expect(onStatus).toHaveBeenCalledWith({ agent: 'up' });
    expect(onNotice).toHaveBeenCalledWith({ kind: 'internet-off', text: 'x' });
    offStatus();
    expect(listeners.get(CHANNELS.statusChanged)!.size).toBe(0);
  });
});

describe('window.localaiDesktopHost (agent pages)', () => {
  function hostIpc(version: unknown = '1.2.3') {
    const invoke = vi.fn(async (_channel: string, ..._args: unknown[]) => ({ ok: false }));
    const send = vi.fn((_channel: string, ..._args: unknown[]) => undefined);
    const sendSync = vi.fn((_channel: string, ..._args: unknown[]) => version);
    const ipc: HostIpcLike = { invoke, send, sendSync };
    return { ...ipc, invoke, send, sendSync };
  }

  it('has exactly the 5 names of the spec (the test that stops the agent bridge from growing)', () => {
    const bridge = createHostBridge(hostIpc());
    expect(Object.keys(bridge).sort()).toEqual([...HOST_MEMBERS].sort());
    expect(Object.keys(bridge)).toHaveLength(5);
    expect(bridge.isDesktop).toBe(true);
    expect(bridge.version).toBe('1.2.3');
  });

  it('a version that is not a string becomes an empty string', () => {
    expect(createHostBridge(hostIpc(null)).version).toBe('');
    expect(createHostBridge(hostIpc({ evil: 1 })).version).toBe('');
  });

  it('calls the host channels, not the shell ones', async () => {
    const ipc = hostIpc();
    const b = createHostBridge(ipc);
    await b.copyApiKey();
    await b.connectionInfo();
    b.openWindow('maps');
    expect(ipc.invoke.mock.calls.map((c) => c[0])).toEqual([CHANNELS.hostCopyApiKey, CHANNELS.hostConnectionInfo]);
    expect(ipc.send).toHaveBeenCalledWith(CHANNELS.hostOpenWindow, 'maps');
    expect(ipc.sendSync).toHaveBeenCalledWith(CHANNELS.hostVersion);
  });
});

describe('window.localaiPin (the PIN window only)', () => {
  it('has submit and cancel and nothing else, on the channels of ipc.ts', async () => {
    const invoke = vi.fn(async (_channel: string, ..._args: unknown[]) => ({ ok: true }));
    const send = vi.fn((_channel: string, ..._args: unknown[]) => undefined);
    const ipc: PinIpcLike = { invoke, send };
    const bridge = createPinBridge(ipc);
    expect(Object.keys(bridge).sort()).toEqual(['cancel', 'submit']);
    await bridge.submit('4321');
    bridge.cancel();
    expect(invoke).toHaveBeenCalledWith(CHANNELS.pinSubmit, '4321');
    expect(send).toHaveBeenCalledWith(CHANNELS.pinCancel);
  });
});
