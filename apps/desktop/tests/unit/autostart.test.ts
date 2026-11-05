import { mkdir, mkdtemp, readFile, readdir, rm, stat, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  type ApplyFailure,
  type ApplyResult,
  type AutostartFs,
  AutostartController,
  DEB_EXEC_PATH,
  applyAutostart,
  autostartDir,
  autostartPath,
  desktopEntry,
  nodeFs,
  quoteExecArg,
  resolveExecPath,
} from '../../src/main/autostart';

// Every test runs against a temp HOME: nothing outside it is ever touched.
let home: string;
beforeEach(async () => {
  home = await mkdtemp(join(tmpdir(), 'ai-desktop-autostart-'));
});
afterEach(async () => {
  await rm(home, { recursive: true, force: true });
});

const env = (extra: Record<string, string> = {}): NodeJS.ProcessEnv => ({ ...extra });
const file = (): string => join(home, '.config', 'autostart', 'ai-desktop.desktop');

const DEB_ENTRY = [
  '[Desktop Entry]',
  'Type=Application',
  'Name=Local AI',
  'Exec=/usr/bin/ai-desktop --hidden',
  'Icon=ai-desktop',
  'X-GNOME-Autostart-enabled=true',
  'X-GNOME-Autostart-Delay=5',
  'NoDisplay=false',
  '',
].join('\n');

describe('where the entry lives', () => {
  it('is ~/.config/autostart/ai-desktop.desktop, or under an absolute XDG_CONFIG_HOME', () => {
    expect(autostartPath(env(), '/home/person-a')).toBe('/home/person-a/.config/autostart/ai-desktop.desktop');
    expect(autostartDir(env({ XDG_CONFIG_HOME: '/srv/cfg' }), '/home/person-a')).toBe('/srv/cfg/autostart');
    expect(autostartDir(env({ XDG_CONFIG_HOME: 'relative/cfg' }), '/home/person-a')).toBe('/home/person-a/.config/autostart');
    expect(autostartDir(env({ XDG_CONFIG_HOME: '' }), '/home/person-a')).toBe('/home/person-a/.config/autostart');
  });
});

describe('file content (desktop.md 4.5)', () => {
  it('the deb entry matches the spec byte for byte', () => {
    expect(desktopEntry(DEB_EXEC_PATH)).toBe(DEB_ENTRY);
  });

  it('the AppImage entry starts the AppImage path with --hidden and changes nothing else', () => {
    const entry = desktopEntry('/home/person-a/Apps/ai-desktop-0.1.0-x86_64.AppImage');
    expect(entry).toBe(DEB_ENTRY.replace('Exec=/usr/bin/ai-desktop --hidden', 'Exec=/home/person-a/Apps/ai-desktop-0.1.0-x86_64.AppImage --hidden'));
  });

  it('quotes an AppImage path with a space, per the Desktop Entry specification', () => {
    const entry = desktopEntry('/home/person-a/My Apps/Local AI.AppImage');
    expect(entry).toContain('\nExec="/home/person-a/My Apps/Local AI.AppImage" --hidden\n');
  });

  it.each([
    ['/opt/plain/path.AppImage', '/opt/plain/path.AppImage'],
    ['/opt/with space/x', '"/opt/with space/x"'],
    ['/opt/tab\there/x', '"/opt/tab\there/x"'],
    ['/opt/100%/x', '/opt/100%%/x'],
    ['/opt/a"b/x', '"/opt/a\\\\"b/x"'],
    ['/opt/$HOME/x', '"/opt/\\\\$HOME/x"'],
    ['/opt/`x`/y', '"/opt/\\\\`x\\\\`/y"'],
    ['/opt/back\\slash/x', '"/opt/back\\\\\\\\slash/x"'],
    ['/opt/(a)/x', '"/opt/(a)/x"'],
    ['/opt/a&b/x', '"/opt/a&b/x"'],
    ['/opt/50% off/x', '"/opt/50%% off/x"'],
  ])('quoteExecArg(%j) is %j', (arg, expected) => {
    expect(quoteExecArg(arg)).toBe(expected);
  });

  it('uses $APPIMAGE when it is an absolute path and the deb launcher otherwise', () => {
    expect(resolveExecPath(env())).toBe('/usr/bin/ai-desktop');
    expect(resolveExecPath(env({ APPIMAGE: '/home/person-a/Apps/x.AppImage' }))).toBe('/home/person-a/Apps/x.AppImage');
    expect(resolveExecPath(env({ APPIMAGE: 'x.AppImage' }))).toBe('/usr/bin/ai-desktop');
    expect(resolveExecPath(env({ APPIMAGE: '' }))).toBe('/usr/bin/ai-desktop');
    expect(resolveExecPath(env({ APPIMAGE: '/home/a/x\nExec=/bin/sh' }))).toBe('/usr/bin/ai-desktop');
  });
});

describe('applyAutostart', () => {
  it('creates the folder and writes the deb entry, mode 0644, leaving no temporary file', async () => {
    expect(await applyAutostart(true, { env: env(), home })).toEqual({ ok: true, action: 'written', path: file() });
    expect(await readFile(file(), 'utf8')).toBe(DEB_ENTRY);
    expect((await stat(file())).mode & 0o777).toBe(0o644);
    expect(await readdir(join(home, '.config', 'autostart'))).toEqual(['ai-desktop.desktop']);
  });

  it('writes the AppImage entry, with the path quoted when it has a space', async () => {
    const appImage = '/home/person-a/My Apps/Local AI.AppImage';
    const result = await applyAutostart(true, { env: env({ APPIMAGE: appImage }), home });
    expect(result.ok).toBe(true);
    expect(await readFile(file(), 'utf8')).toBe(desktopEntry(appImage));
    expect(await readFile(file(), 'utf8')).toContain(`Exec="${appImage}" --hidden\n`);
  });

  it('forces 0644 even when the umask narrowed the temporary file', async () => {
    const chmod = vi.fn(nodeFs.chmod);
    await applyAutostart(true, { env: env(), home, fs: { ...nodeFs, chmod } });
    expect(chmod).toHaveBeenCalledWith(expect.stringContaining('.tmp'), 0o644);
  });

  it('is atomic: the entry only ever appears through a rename of the temporary file', async () => {
    const calls: string[] = [];
    const fs: AutostartFs = {
      ...nodeFs,
      writeFile: async (path, data, options) => {
        calls.push(`write ${path.endsWith('.tmp') ? 'tmp' : 'target'}`);
        await nodeFs.writeFile(path, data, options);
      },
      rename: async (from, to) => {
        calls.push(`rename ${from.endsWith('.tmp') ? 'tmp' : 'other'} -> ${to.endsWith('ai-desktop.desktop') ? 'target' : 'other'}`);
        await nodeFs.rename(from, to);
      },
    };
    await applyAutostart(true, { env: env(), home, fs });
    expect(calls).toEqual(['write tmp', 'rename tmp -> target']);
  });

  it('re-applying a correct entry changes nothing (no write at all)', async () => {
    await applyAutostart(true, { env: env(), home });
    const writeFile = vi.fn(nodeFs.writeFile);
    const rename = vi.fn(nodeFs.rename);
    const again = await applyAutostart(true, { env: env(), home, fs: { ...nodeFs, writeFile, rename } });
    expect(again).toMatchObject({ ok: true, action: 'unchanged' });
    expect(writeFile).not.toHaveBeenCalled();
    expect(rename).not.toHaveBeenCalled();
  });

  it('an AppImage that moved fixes its Exec line at the next start', async () => {
    await applyAutostart(true, { env: env({ APPIMAGE: '/home/person-a/old/x.AppImage' }), home });
    const moved = await applyAutostart(true, { env: env({ APPIMAGE: '/home/person-a/new/x.AppImage' }), home });
    expect(moved).toMatchObject({ ok: true, action: 'written' });
    expect(await readFile(file(), 'utf8')).toContain('Exec=/home/person-a/new/x.AppImage --hidden\n');
  });

  it('replaces a damaged entry', async () => {
    await mkdir(join(home, '.config', 'autostart'), { recursive: true });
    await writeFile(file(), 'garbage');
    expect(await applyAutostart(true, { env: env(), home })).toMatchObject({ ok: true, action: 'written' });
    expect(await readFile(file(), 'utf8')).toBe(DEB_ENTRY);
  });

  it('disabling removes the entry and never touches the other autostart files', async () => {
    const dir = join(home, '.config', 'autostart');
    await mkdir(dir, { recursive: true });
    await writeFile(join(dir, 'other-app.desktop'), '[Desktop Entry]\nName=Other\n');
    await writeFile(join(dir, 'ai-desktop-notes.desktop'), 'keep me');
    await applyAutostart(true, { env: env(), home });
    expect(await applyAutostart(false, { env: env(), home })).toEqual({ ok: true, action: 'removed', path: file() });
    expect((await readdir(dir)).sort()).toEqual(['ai-desktop-notes.desktop', 'other-app.desktop']);
    expect(await readFile(join(dir, 'other-app.desktop'), 'utf8')).toBe('[Desktop Entry]\nName=Other\n');
  });

  it('disabling when there is nothing to remove (not even the folder) succeeds and creates nothing', async () => {
    expect(await applyAutostart(false, { env: env(), home })).toEqual({ ok: true, action: 'absent', path: file() });
    expect(await readdir(home)).toEqual([]);
  });

  it('only ever touches the entry file and its temporary file', async () => {
    const touched = new Set<string>();
    const seen = (p: string): string => {
      touched.add(p);
      return p;
    };
    const fs: AutostartFs = {
      mkdir: (p, o) => nodeFs.mkdir(seen(p), o),
      readFile: (p, e) => nodeFs.readFile(seen(p), e),
      writeFile: (p, d, o) => nodeFs.writeFile(seen(p), d, o),
      chmod: (p, m) => nodeFs.chmod(seen(p), m),
      rename: (a, b) => nodeFs.rename(seen(a), seen(b)),
      rm: (p, o) => nodeFs.rm(seen(p), o),
    };
    await applyAutostart(true, { env: env(), home, fs });
    await applyAutostart(false, { env: env(), home, fs });
    for (const p of touched) expect(p.startsWith(join(home, '.config', 'autostart')), p).toBe(true);
    const files = [...touched].filter((p) => p !== join(home, '.config', 'autostart'));
    for (const p of files) expect(p === file() || p.startsWith(`${file()}.`), p).toBe(true);
  });

  describe('LAI-DSK-014', () => {
    it('is reported with the path when the folder cannot be created (a file is in the way)', async () => {
      await mkdir(join(home, '.config'), { recursive: true });
      await writeFile(join(home, '.config', 'autostart'), 'not a folder');
      const result = (await applyAutostart(true, { env: env(), home })) as ApplyFailure;
      expect(result.ok).toBe(false);
      expect(result.code).toBe('LAI-DSK-014');
      expect(result.path).toBe(file());
      expect(result.reason).toMatch(/^E[A-Z]+$/);
    });

    it('is reported on a permission error and the temporary file is cleaned up', async () => {
      const rename = vi.fn().mockRejectedValue(Object.assign(new Error('permission denied'), { code: 'EACCES' }));
      const result = await applyAutostart(true, { env: env(), home, fs: { ...nodeFs, rename } });
      expect(result).toEqual({ ok: false, code: 'LAI-DSK-014', path: file(), reason: 'EACCES' });
      expect(await readdir(join(home, '.config', 'autostart'))).toEqual([]);
    });

    it('is reported on a read-only file system', async () => {
      const mkdirFails = vi.fn().mockRejectedValue(Object.assign(new Error('read-only'), { code: 'EROFS' }));
      expect(await applyAutostart(true, { env: env(), home, fs: { ...nodeFs, mkdir: mkdirFails } })).toMatchObject({ ok: false, code: 'LAI-DSK-014', reason: 'EROFS' });
    });

    it('is reported when the entry cannot be removed', async () => {
      await applyAutostart(true, { env: env(), home });
      const rm = vi.fn().mockRejectedValue(Object.assign(new Error('permission denied'), { code: 'EACCES' }));
      expect(await applyAutostart(false, { env: env(), home, fs: { ...nodeFs, rm } })).toEqual({ ok: false, code: 'LAI-DSK-014', path: file(), reason: 'EACCES' });
    });

    it('a correct entry in a folder that cannot be written is not an error', async () => {
      await applyAutostart(true, { env: env(), home });
      const mkdirFails = vi.fn().mockRejectedValue(Object.assign(new Error('x'), { code: 'EACCES' }));
      const writeFails = vi.fn().mockRejectedValue(Object.assign(new Error('x'), { code: 'EACCES' }));
      expect(await applyAutostart(true, { env: env(), home, fs: { ...nodeFs, mkdir: mkdirFails, writeFile: writeFails } })).toMatchObject({ ok: true, action: 'unchanged' });
    });

    it('logs the code and never the content of the file', async () => {
      const lines: Array<{ msg: string; fields?: Record<string, unknown> }> = [];
      const log = { write: () => undefined, log: (_l: string, _c: string, msg: string, fields?: Record<string, unknown>) => void lines.push({ msg, fields }) };
      const rename = vi.fn().mockRejectedValue(Object.assign(new Error('x'), { code: 'EACCES' }));
      await applyAutostart(true, { env: env(), home, fs: { ...nodeFs, rename }, log });
      expect(lines).toHaveLength(1);
      expect(lines[0]!.fields).toEqual({ code: 'LAI-DSK-014' });
    });
  });
});

describe('AutostartController ("Start at login" in the tray)', () => {
  const ok = (action: 'written' | 'removed' = 'written'): ApplyResult => ({ ok: true, action, path: '/p' });
  const failure: ApplyFailure = { ok: false, code: 'LAI-DSK-014', path: '/p', reason: 'EACCES' };

  function make(wanted: boolean, apply: (enabled: boolean) => Promise<ApplyResult>) {
    const persisted: boolean[] = [];
    const failures: Array<[ApplyFailure, boolean]> = [];
    const controller = new AutostartController({
      wanted,
      apply,
      persist: async (value) => void persisted.push(value),
      onFailure: (f, w) => void failures.push([f, w]),
    });
    return { controller, persisted, failures };
  }

  it('applies the setting at start-up (first run: on) without rewriting the setting', async () => {
    const apply = vi.fn().mockResolvedValue(ok());
    const { controller, persisted } = make(true, apply);
    expect(controller.enabled).toBe(true);
    await controller.applyAtStart();
    expect(apply).toHaveBeenCalledWith(true);
    expect(controller.enabled).toBe(true);
    expect(persisted).toEqual([]);
  });

  it('a user who turned it off keeps it off: start-up removes the entry', async () => {
    const apply = vi.fn().mockResolvedValue(ok('removed'));
    const { controller } = make(false, apply);
    await controller.applyAtStart();
    expect(apply).toHaveBeenCalledWith(false);
    expect(controller.enabled).toBe(false);
  });

  it('a start-up that cannot write shows the toast, shows the item unticked, and keeps the wish for the next start', async () => {
    const { controller, persisted, failures } = make(true, async () => failure);
    await controller.applyAtStart();
    expect(failures).toEqual([[failure, true]]);
    expect(controller.enabled).toBe(false);
    expect(persisted).toEqual([]);
  });

  it('toggling off and on writes the setting after the file', async () => {
    const order: string[] = [];
    const { controller, persisted } = make(true, async (enabled) => {
      order.push(`apply ${enabled}`);
      return ok(enabled ? 'written' : 'removed');
    });
    const originalPersist = persisted.push.bind(persisted);
    persisted.push = (...values: boolean[]) => {
      order.push(`persist ${values[0]}`);
      return originalPersist(...values);
    };
    await controller.set(false);
    expect(controller.enabled).toBe(false);
    await controller.set(true);
    expect(controller.enabled).toBe(true);
    expect(order).toEqual(['apply false', 'persist false', 'apply true', 'persist true']);
  });

  it('turning it on when the file cannot be written: toast, still off, and the setting is not changed', async () => {
    const { controller, persisted, failures } = make(false, async () => failure);
    const result = await controller.set(true);
    expect(result.ok).toBe(false);
    expect(controller.enabled).toBe(false);
    expect(persisted).toEqual([]);
    expect(failures).toEqual([[failure, true]]);
  });

  it('turning it off when the file cannot be removed: toast, the choice is remembered and retried at every start', async () => {
    const { controller, persisted, failures } = make(true, async () => failure);
    await controller.set(false);
    expect(controller.enabled).toBe(false);
    expect(persisted).toEqual([false]);
    expect(failures).toEqual([[failure, false]]);
  });

  it('a settings file that cannot be written does not break the toggle', async () => {
    const controller = new AutostartController({
      wanted: false,
      apply: async () => ok(),
      persist: async () => {
        throw new Error('disk full');
      },
      onFailure: () => undefined,
    });
    expect((await controller.set(true)).ok).toBe(true);
    expect(controller.enabled).toBe(true);
  });

  it('serialises quick clicks: the last one wins and no two writes overlap', async () => {
    let running = 0;
    let overlapped = false;
    const apply = async (enabled: boolean): Promise<ApplyResult> => {
      running += 1;
      overlapped ||= running > 1;
      await new Promise((r) => setTimeout(r, 5));
      running -= 1;
      return ok(enabled ? 'written' : 'removed');
    };
    const { controller, persisted } = make(true, apply);
    await Promise.all([controller.set(false), controller.set(true), controller.set(false)]);
    expect(overlapped).toBe(false);
    expect(persisted).toEqual([false, true, false]);
    expect(controller.enabled).toBe(false);
  });
});
