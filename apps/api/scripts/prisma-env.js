#!/usr/bin/env node
// Runs the Prisma CLI with DATABASE_URL built from POSTGRES_* (the variables
// infra/compose/.env defines), through the platform's own builder so the CLI
// and the API connect to the same database. `npm run prisma:<x>` calls this.
const { spawnSync } = require('node:child_process');
const { buildDatabaseUrl } = require('@marinoscar/platform-api/core');

if (!process.env.DATABASE_URL) process.env.DATABASE_URL = buildDatabaseUrl(process.env);
const result = spawnSync('prisma', process.argv.slice(2), { stdio: 'inherit', shell: process.platform === 'win32' });
process.exitCode = result.status ?? 1;
