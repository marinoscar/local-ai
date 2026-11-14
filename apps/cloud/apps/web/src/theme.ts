import { createTheme, type Theme } from '@mui/material/styles';
import { createShellTheme } from '@marinoscar/platform-web/shell/headless';
import { withTelemetryTokens } from '@marinoscar/platform-web/telemetry/headless';

/**
 * Corona, the Local AI theme (ADR-0014, docs/decisions/0014-corona-identity.md).
 *
 * The values are the dark tokens of packages/ui/brand/tokens.json (the repository's one source; `theme.test.ts`
 * fails when they drift). They are copied, not imported, because the web app is built from apps/cloud alone.
 *
 * identity.json `themeColor` is NOT the brand colour any more: it is the browser and installed-PWA chrome colour
 * (`<meta name="theme-color">`, the manifest's theme_color and background_color), the near-black `bg` token, so the
 * title bar and the splash screen match the dark app. The brand accent, gold, is `CORONA.gold` below.
 */
export const CORONA = {
  bg: '#07070A',
  surface: '#111015',
  raised: '#1F1D26',
  line: '#2A2833',
  text: '#F5EFE3',
  textMuted: '#B3ACBF',
  gold: '#F2B544',
  goldHover: '#FFD27A',
  goldLight: '#FFE3A3',
  onGold: '#1A1206',
  ok: '#4ADE80',
  warn: '#F5A524',
  bad: '#F87171',
  info: '#7DB6FF',
} as const;

/**
 * The light theme's gold: dark enough to be text on white (5.9:1 on #ffffff, 5.4:1 on the shell's #f5f5f5), and white
 * text on it as a button (5.9:1). The light theme stays available from the theme toggle; it is not the default.
 */
export const LIGHT_GOLD = '#8A5A00';

/** Where the shell keeps the viewer's light / dark / system choice (the platform's default key, named here). */
export const THEME_STORAGE_KEY = 'theme_mode';

/** The 3 px gold focus ring of ADR-0014 §4, on every focusable element, in both themes. */
function withFocusRing(ring: string) {
  return (theme: Theme): Theme =>
    createTheme(theme, {
      components: {
        MuiCssBaseline: {
          styleOverrides: { '*:focus-visible': { outline: `3px solid ${ring}`, outlineOffset: '2px' } },
        },
      },
    });
}

const darkCorona = (theme: Theme): Theme =>
  createTheme(theme, {
    components: {
      // The shell draws a #333 hairline under the bar; Corona's line token instead.
      MuiAppBar: { styleOverrides: { root: { borderBottom: `1px solid ${CORONA.line}` } } },
    },
  });

/**
 * The app owns its theme: the platform shell's light and dark themes with the Corona palette, plus the telemetry
 * tokens the packaged pages read. Change the palette here, not in the packages.
 */
export const APP_THEMES = {
  light: createShellTheme('light', {
    palette: { primary: { main: LIGHT_GOLD, contrastText: '#ffffff' }, background: { default: '#ffffff' } },
    extend: (theme) => withTelemetryTokens(withFocusRing(LIGHT_GOLD)(theme)),
  }),
  dark: createShellTheme('dark', {
    palette: {
      primary: { main: CORONA.gold, light: CORONA.goldLight, dark: CORONA.goldHover, contrastText: CORONA.onGold },
      background: { default: CORONA.bg, paper: CORONA.surface },
      text: { primary: CORONA.text, secondary: CORONA.textMuted },
      divider: CORONA.line,
      success: { main: CORONA.ok },
      warning: { main: CORONA.warn },
      error: { main: CORONA.bad },
      info: { main: CORONA.info },
    },
    extend: (theme) => withTelemetryTokens(darkCorona(withFocusRing(CORONA.gold)(theme))),
  }),
};

/**
 * Corona opens dark (ADR-0014, Consequences). The platform's `ShellRoot` starts at `system` when this browser has no
 * stored choice and has no prop for another default, so the app stores `dark` before the first render when nothing is
 * stored yet. A choice the viewer made (light, dark or system) is never overwritten. Blocked storage (a private
 * window) leaves the platform's `system` default.
 */
export function seedDarkDefault(storage: Pick<Storage, 'getItem' | 'setItem'> | undefined = globalThis.localStorage): void {
  try {
    if (storage && storage.getItem(THEME_STORAGE_KEY) === null) storage.setItem(THEME_STORAGE_KEY, 'dark');
  } catch {
    // Storage blocked: the shell falls back to `system`.
  }
}
