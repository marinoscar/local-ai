import { describe, expect, it } from 'vitest';
import { NO_ARGS, parseArgs } from '../../src/main/args';

const BIN = '/opt/ai-desktop/ai-desktop';

describe('command line (desktop.md 3.9)', () => {
  it.each([
    ['no flags', [BIN], { hidden: false, portal: false, diagnose: false }],
    ['--hidden', [BIN, '--hidden'], { hidden: true, portal: false, diagnose: false }],
    ['--portal', [BIN, '--portal'], { hidden: false, portal: true, diagnose: false }],
    ['--diagnose', [BIN, '--diagnose'], { hidden: false, portal: false, diagnose: true }],
    ['two flags, any order', [BIN, '--portal', '--hidden'], { hidden: true, portal: true, diagnose: false }],
    ['all three', [BIN, '--diagnose', '--hidden', '--portal'], { hidden: true, portal: true, diagnose: true }],
    ['repeated', [BIN, '--hidden', '--hidden'], { hidden: true, portal: false, diagnose: false }],
    ['an unpackaged run: the app folder comes first', ['/usr/lib/electron/electron', '/home/person-a/ai-desktop', '--hidden'], { hidden: true, portal: false, diagnose: false }],
    ['Chromium switches and unknown flags are ignored', [BIN, '--no-sandbox', '--disable-gpu', '--ozone-platform=headless', '--frobnicate', '-h', '--hidden'], { hidden: true, portal: false, diagnose: false }],
    ['a flag with a value is another flag', [BIN, '--hidden=true', '--portal=1', '--diagnose=yes'], { hidden: false, portal: false, diagnose: false }],
    ['case matters, prefixes do not count', [BIN, '--Hidden', '--hide', '--hidden-now', 'hidden', '-hidden'], { hidden: false, portal: false, diagnose: false }],
    ['nothing after a bare --', [BIN, '--hidden', '--', '--portal', '--diagnose'], { hidden: true, portal: false, diagnose: false }],
    ['an empty argv', [], { hidden: false, portal: false, diagnose: false }],
  ])('%s', (_name, argv, expected) => {
    expect(parseArgs(argv)).toEqual(expected);
  });

  it('returns a fresh object each time and leaves the defaults alone', () => {
    const first = parseArgs([BIN, '--hidden']);
    first.portal = true;
    expect(parseArgs([BIN])).toEqual(NO_ARGS);
    expect(NO_ARGS).toEqual({ hidden: false, portal: false, diagnose: false });
  });
});
