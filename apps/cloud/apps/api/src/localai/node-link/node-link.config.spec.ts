// The config handshake body (docs/specs/cloud-app.md section 3.6) against the shared fixture the Go agent's fake
// cloud replays too (`test/fixtures/localai/config.response.json`).
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import { LocalaiConfigResponseSchema } from '../contracts/config.schema';
import { buildLocalaiConfig } from './node-link.config';

const fixture = JSON.parse(readFileSync(join(__dirname, '../../../test/fixtures/localai/config.response.json'), 'utf8')) as {
  data: Record<string, unknown>;
  meta: { timestamp: string };
};
const NOW = new Date(fixture.meta.timestamp);

describe('buildLocalaiConfig', () => {
  it('returns exactly the fixture body for a node whose key was reported', () => {
    expect(buildLocalaiConfig({ nodeId: 'ai-n3', keyReportedAt: new Date('2026-11-01T00:00:00Z') }, NOW)).toEqual(fixture.data);
  });

  it('keeps the key order and field set of the fixture (the wire contract)', () => {
    const built = buildLocalaiConfig({ nodeId: 'ai-n3', keyReportedAt: new Date() }, NOW);
    expect(Object.keys(built)).toEqual(Object.keys(fixture.data));
  });

  it('keyReported is false until keyReportedAt is set', () => {
    expect(buildLocalaiConfig({ nodeId: 'ai-n4', keyReportedAt: null }, NOW)).toMatchObject({ nodeId: 'ai-n4', keyReported: false });
  });

  it('serverTime is the clock it was given, as ISO 8601 UTC with milliseconds', () => {
    expect(buildLocalaiConfig({ nodeId: 'ai-n3', keyReportedAt: null }, new Date('2026-11-04T08:09:10.011Z')).serverTime).toBe('2026-11-04T08:09:10.011Z');
  });

  it('the fixture itself satisfies the strict schema, and an unknown key does not', () => {
    expect(LocalaiConfigResponseSchema.safeParse(fixture.data).success).toBe(true);
    expect(LocalaiConfigResponseSchema.safeParse({ ...fixture.data, extra: 1 }).success).toBe(false);
    expect(LocalaiConfigResponseSchema.safeParse({ ...fixture.data, commandPlane: 'x' }).success).toBe(false);
  });
});
