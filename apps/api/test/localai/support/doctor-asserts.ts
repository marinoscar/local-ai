// The acceptance criterion of CLOUD-17 as one helper: no Doctor outcome carries a 64-hex string, a `nod_`/`lai_`/`pat_`
// token or an IPv4 address. Every value used to provoke a leak is built at run time, never committed.
import { randomBytes } from 'node:crypto';
import type { DoctorCheckOutcome } from '@marinoscar/platform-api/doctor';

export const HEX64_RE = /\b[0-9a-fA-F]{64}\b/;
export const TOKEN_RE = /\b(?:nod|pat|lai)_[A-Za-z0-9_-]{6,}/;
export const IPV4_RE = /\b(?:\d{1,3}\.){3}\d{1,3}\b/;

export function expectNoSecretShapes(value: unknown): void {
  const text = typeof value === 'string' ? value : JSON.stringify(value);
  expect(text).not.toMatch(HEX64_RE);
  expect(text).not.toMatch(TOKEN_RE);
  expect(text).not.toMatch(IPV4_RE);
}

/** Throwaway values shaped like the things that must never leak. */
export function leakBait() {
  return {
    hex64: randomBytes(32).toString('hex'),
    token: `nod_${randomBytes(24).toString('hex')}`,
    // Built from numbers so no address literal sits in the source.
    ip: [100, randomBytes(1)[0], randomBytes(1)[0], 1 + (randomBytes(1)[0] % 250)].join('.'),
  };
}

/** What a valid outcome looks like to the service: one line, a remedy on warn/fail. */
export function expectContract(outcome: DoctorCheckOutcome): void {
  expect(['pass', 'warn', 'fail', 'skip']).toContain(outcome.status);
  expect(outcome.detail).not.toMatch(/\n/);
  expect(outcome.detail.length).toBeGreaterThan(0);
  if (outcome.status === 'warn' || outcome.status === 'fail') expect((outcome.remedy ?? '').length).toBeGreaterThan(0);
  expectNoSecretShapes(outcome);
}
