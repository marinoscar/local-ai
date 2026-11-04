import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';

import { APP_NAME, CLI_NAME } from '@app/shared';
import { createCli } from '@marinoscar/platform-cli';
import { PLATFORM_DOCUMENTED_OPTIONAL_KEYS, resetCliForTests, runPlatformConformance } from '@marinoscar/platform-cli/testing';
import { describe, expect, it } from 'vitest';

import { APP_CLI_OPTIONS } from './app.js';

const read = (url: URL) => readFileSync(url, 'utf8');
const baseEnv = createRequire(import.meta.url).resolve('@marinoscar/platform-infra/package.json').replace(/package\.json$/, 'env/base.env.example');

resetCliForTests();
createCli(APP_CLI_OPTIONS);

// The platform's CLI invariants against this composition: the env-spec
// fragments (the platform's and infra/compose/app.env.example) parse, no key
// is owned twice, every executor and screen is registered once.
runPlatformConformance({
  suites: {
    cli: {
      envFragments: [
        {
          name: '@marinoscar/platform-infra/env/base.env.example',
          text: readFileSync(baseEnv, 'utf8'),
          allowCommented: PLATFORM_DOCUMENTED_OPTIONAL_KEYS,
        },
        { name: 'infra/compose/app.env.example', text: read(new URL('../../../infra/compose/app.env.example', import.meta.url)) },
      ],
    },
  },
  testApi: { describe, it, expect },
});

describe('the CLI identity', () => {
  it('takes its name from identity.json, and package.json bin is that one name', () => {
    const manifest = JSON.parse(read(new URL('../package.json', import.meta.url))) as { bin: Record<string, string> };
    expect(APP_CLI_OPTIONS.identity.name).toBe(CLI_NAME);
    expect(Object.keys(manifest.bin)).toEqual([CLI_NAME]);
    expect(APP_CLI_OPTIONS.identity.productName).toBe(APP_NAME);
  });

  it('names itself in --help', async () => {
    resetCliForTests();
    const cli = createCli(APP_CLI_OPTIONS);
    let out = '';
    const write = process.stdout.write.bind(process.stdout);
    process.stdout.write = ((chunk: string | Uint8Array) => {
      out += String(chunk);
      return true;
    }) as typeof process.stdout.write;
    try {
      expect(await cli.run(['--help'])).toBe(0);
    } finally {
      process.stdout.write = write;
    }
    expect(out).toContain(`Usage: ${CLI_NAME}`);
  });
});
