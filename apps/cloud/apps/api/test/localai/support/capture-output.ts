// Test support: capture every place a process writes a log line while a flow runs, so a tripwire can assert that no
// 64-hex string (a llama key) reached any of them. Covers `process.stdout`/`process.stderr` (Nest's ConsoleLogger,
// the platform's logger and Fastify's logger write there) and `console.*`.
import { randomBytes } from 'node:crypto';
import { ConsoleLogger, Logger } from '@nestjs/common';
import { TestingLogger } from '@nestjs/testing/services/testing-logger.service';

export interface CapturedOutput {
  /** Everything written since `captureOutput()`, joined. */
  text(): string;
  restore(): void;
}

/**
 * `Test.createTestingModule` silences Nest's logger to errors only, which would make a log tripwire vacuous. This turns
 * the full console logger on (as in production) and returns the function that puts the testing logger back.
 */
export function enableNestLogging(): () => void {
  Logger.overrideLogger(new ConsoleLogger());
  return () => Logger.overrideLogger(new TestingLogger());
}

export function captureOutput(): CapturedOutput {
  const chunks: string[] = [];
  const push = (value: unknown) => chunks.push(typeof value === 'string' ? value : Buffer.isBuffer(value) ? value.toString('utf8') : JSON.stringify(value));
  const stdoutWrite = process.stdout.write;
  const stderrWrite = process.stderr.write;
  process.stdout.write = function (this: unknown, chunk: unknown, ...rest: unknown[]) {
    push(chunk);
    return (stdoutWrite as (...a: unknown[]) => boolean).call(process.stdout, chunk, ...rest);
  } as typeof process.stdout.write;
  process.stderr.write = function (this: unknown, chunk: unknown, ...rest: unknown[]) {
    push(chunk);
    return (stderrWrite as (...a: unknown[]) => boolean).call(process.stderr, chunk, ...rest);
  } as typeof process.stderr.write;
  const originals = new Map<string, unknown>();
  for (const level of ['log', 'info', 'warn', 'error', 'debug'] as const) {
    originals.set(level, console[level]);
    console[level] = (...args: unknown[]) => {
      push(args.map((a) => (typeof a === 'string' ? a : safeJson(a))).join(' '));
    };
  }
  return {
    text: () => chunks.join('\n'),
    restore() {
      process.stdout.write = stdoutWrite;
      process.stderr.write = stderrWrite;
      for (const [level, fn] of originals) (console as unknown as Record<string, unknown>)[level] = fn;
    },
  };
}

function safeJson(value: unknown): string {
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

/** A runtime-built llama key: 64 lowercase hex. Tests never commit a key literal. */
export function testKey(): string {
  return randomBytes(32).toString('hex');
}

/** Any run of 64 hex characters: what a llama key looks like inside text. */
export const HEX64 = /[0-9a-f]{64}/;
