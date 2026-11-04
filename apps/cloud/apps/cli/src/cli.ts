#!/usr/bin/env node
import { createCli, EXIT, exitCodeFor, formatError } from '@marinoscar/platform-cli';

import { APP_CLI_OPTIONS } from './app.js';

// Set exitCode and return; never process.exit(), which truncates piped output.
async function main(): Promise<void> {
  let cli: ReturnType<typeof createCli>;
  try {
    cli = createCli(APP_CLI_OPTIONS);
  } catch (error) {
    process.stderr.write(`${formatError(error)}\n`);
    process.exitCode = exitCodeFor(error);
    return;
  }
  process.exitCode = await cli.run(process.argv.slice(2));
}

process.on('unhandledRejection', (reason) => {
  process.stderr.write(`${formatError(reason)}\n`);
  process.exitCode = EXIT.FAILURE;
});

void main();
