// The command line of ai-desktop, parsed in one place (docs/specs/desktop.md 3.9).
//   ai-desktop              normal start
//   ai-desktop --hidden     start in the tray without showing the main window (written by the autostart entry)
//   ai-desktop --portal     open the Survival Portal full screen (a second launch forwards it to the running app)
//   ai-desktop --diagnose   print the checks as JSON and exit, no window
// No Electron import. Anything else on the line (Chromium's own switches, the app folder of an unpackaged run, flags of a
// newer version) is ignored, and so is everything after a bare `--`.

export const MODULE = 'args' as const;

export interface LaunchArgs {
  hidden: boolean;
  portal: boolean;
  diagnose: boolean;
}

export const NO_ARGS: Readonly<LaunchArgs> = Object.freeze({ hidden: false, portal: false, diagnose: false });

/** Exact matches only: `--hidden=1` or `--hide` is an unknown flag. `argv` is `process.argv` or a second launch's argv. */
export function parseArgs(argv: readonly string[]): LaunchArgs {
  const result: LaunchArgs = { ...NO_ARGS };
  for (const arg of argv) {
    if (arg === '--') break;
    if (arg === '--hidden') result.hidden = true;
    else if (arg === '--portal') result.portal = true;
    else if (arg === '--diagnose') result.diagnose = true;
  }
  return result;
}
