// The identity defaults the cloud deploys with (docs/specs/cloud-app.md §3.4,
// §3.13), read from the generated `infra/compose/.env.example` and the
// app-owned `infra/compose/app.env.example`. Only placeholders may be committed.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const COMPOSE = join(__dirname, '..', '..', '..', 'infra', 'compose');
const ASSIGNMENT = /^([A-Za-z_][A-Za-z0-9_]*)=(.*)$/;
const EMAIL = /[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[a-z]{2,}/g;

function assignments(file: string): Array<[string, string]> {
  return readFileSync(join(COMPOSE, file), 'utf8')
    .split(/\r?\n/)
    .flatMap((line) => {
      const match = ASSIGNMENT.exec(line);
      return match ? [[match[1], match[2]] as [string, string]] : [];
    });
}

describe('the cloud identity environment', () => {
  const template = assignments('.env.example');

  it('runs single-tenant', () => {
    expect(template.filter(([key]) => key === 'TENANCY_MODE')).toEqual([['TENANCY_MODE', 'single']]);
  });

  it('declares each identity variable exactly once (the deploy wizard shows a duplicate twice)', () => {
    for (const key of ['INITIAL_ADMIN_EMAIL', 'GOOGLE_CALLBACK_URL', 'GOOGLE_CLIENT_ID', 'GOOGLE_CLIENT_SECRET', 'TENANCY_MODE']) {
      expect(template.filter(([name]) => name === key)).toHaveLength(1);
    }
  });

  it('commits no real credential for Google sign-in', () => {
    const value = (key: string) => template.find(([name]) => name === key)?.[1];
    expect(value('GOOGLE_CLIENT_ID')).toBe('your-google-client-id');
    expect(value('GOOGLE_CLIENT_SECRET')).toBe('your-google-client-secret');
  });

  it('puts no address but a placeholder in the app-owned template', () => {
    const app = readFileSync(join(COMPOSE, 'app.env.example'), 'utf8');
    expect(app.match(EMAIL) ?? []).toEqual([]);
  });
});
