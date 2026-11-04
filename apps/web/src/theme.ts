import { BACKGROUND_COLOR, THEME_COLOR } from '@app/shared';
import { createShellTheme } from '@marinoscar/platform-web/shell/headless';
import { withTelemetryTokens } from '@marinoscar/platform-web/telemetry/headless';

/**
 * The app owns its theme: the platform shell's light and dark themes with the
 * brand colour from identity.json, plus the telemetry tokens the packaged
 * pages read. Change the palette here, not in the packages.
 */
export const APP_THEMES = {
  light: createShellTheme('light', {
    palette: { primary: { main: THEME_COLOR }, background: { default: BACKGROUND_COLOR } },
    extend: withTelemetryTokens,
  }),
  dark: createShellTheme('dark', { extend: withTelemetryTokens }),
};
