// The command history never shows a music request's lyrics or signed URLs (E11): `redactParams` turns them into sizes
// and flags, and leaves every other type as stored.
import type { LocalaiCommand } from '@prisma/client';

import { redactParams, toCommandView } from './command-view';
import { readCommandExamples } from '../../../test/fixtures/localai/command-examples';

const examples = readCommandExamples<Record<string, { params: Record<string, unknown> }>>();
const music = examples['localai.music.generate'].params;

describe('redactParams', () => {
  it('music.generate: no lyrics, no tags, no URL, no sha256; sizes and flags instead', () => {
    const shown = redactParams('localai.music.generate', music);
    expect(shown).toEqual({
      request_id: music.request_id,
      lyrics_bytes: new TextEncoder().encode(music.lyrics as string).length,
      duration_s: 60,
      language: 'en',
      variations: 2,
      has_sample: true,
      sample_mode: 'sounds_like',
      results: 2,
    });
    const text = JSON.stringify(shown);
    expect(text).not.toContain('Person A');
    expect(text).not.toContain('https://');
    expect(text).not.toContain('folk');
  });

  it('leaves every other type as stored', () => {
    for (const [type, { params }] of Object.entries(examples)) {
      if (type === 'localai.music.generate') continue;
      expect(redactParams(type, params)).toBe(params);
    }
  });

  it('toCommandView applies it', () => {
    const row = {
      id: 'c1', nodeId: 'n1', type: 'localai.music.generate', status: 'queued', params: music, source: 'ui', requestedById: null, attempts: 0,
      expiresAt: new Date('2026-11-14T12:00:00Z'), createdAt: new Date('2026-11-14T11:45:00Z'), claimedAt: null, finishedAt: null, error: null, result: null,
    } as unknown as LocalaiCommand;
    expect(JSON.stringify(toCommandView(row, 'ai-n2', { withResult: true }))).not.toContain('X-Amz-Signature');
  });
});
