import { describe, expect, it } from 'vitest';

import { KNOWN_CODES, codeFix, codeMeaning, toErrorInfo } from '../common/errors';
import { WORKLOAD_STRINGS } from '../workloads/strings';
import { fileNameOf, formatBytes, formatDuration } from './format';
import { MUSIC_STRINGS } from './strings';
import { defaultStrength, insertSection, toRequest, validateForm, type StudioForm } from './validate';

const base: StudioForm = {
  lyrics: 'la la',
  tags: '',
  duration: '60',
  language: 'en',
  seed: '',
  variations: '1',
  sample: null,
  sampleMode: 'sounds_like',
  strength: null,
};

describe('validateForm', () => {
  it('accepts a minimal form', () => {
    expect(validateForm(base)).toEqual([]);
    expect(validateForm({ ...base, lyrics: '', tags: 'piano' })).toEqual([]);
  });

  it('needs lyrics or a style', () => {
    expect(validateForm({ ...base, lyrics: '  ', tags: '' }).map((e) => e.key)).toEqual(['errNothing']);
  });

  it('counts the limits in UTF-8 bytes, like the node', () => {
    expect(validateForm({ ...base, lyrics: 'a'.repeat(8192) })).toEqual([]);
    expect(validateForm({ ...base, lyrics: 'ñ'.repeat(4097) }).map((e) => e.key)).toEqual(['errLyricsLong']);
    expect(validateForm({ ...base, tags: 'x'.repeat(513) }).map((e) => e.key)).toEqual(['errTagsLong']);
  });

  it('checks length, versions and seed', () => {
    for (const d of ['9', '241', '', '60.5', 'abc']) expect(validateForm({ ...base, duration: d }).map((e) => e.field)).toEqual(['duration_s']);
    for (const d of ['10', '240']) expect(validateForm({ ...base, duration: d })).toEqual([]);
    for (const v of ['0', '5']) expect(validateForm({ ...base, variations: v }).map((e) => e.field)).toEqual(['variations']);
    for (const s of ['-1', '1.5', 'x', '4294967296']) expect(validateForm({ ...base, seed: s }).map((e) => e.field)).toEqual(['seed']);
    expect(validateForm({ ...base, seed: '4294967295' })).toEqual([]);
  });
});

describe('toRequest', () => {
  it('leaves the sample fields null without a sample', () => {
    expect(toRequest({ ...base, strength: 0.9 })).toMatchObject({ sample: null, sample_mode: null, strength: null, seed: null, duration_s: 60 });
  });

  it('uses the mode default strength unless one was chosen', () => {
    const sample = new File(['x'], 'a.wav');
    expect(toRequest({ ...base, sample, sampleMode: 'same_song' }).strength).toBe(1);
    expect(toRequest({ ...base, sample, sampleMode: 'sounds_like' }).strength).toBe(0.5);
    expect(toRequest({ ...base, sample, strength: 0.25 }).strength).toBe(0.25);
    expect(defaultStrength('same_song')).toBe(1);
  });
});

describe('insertSection', () => {
  it('puts the tag on its own line', () => {
    expect(insertSection('', 0, 0, 'verse')).toEqual({ text: '[verse]\n', caret: 8 });
    expect(insertSection('abc', 3, 3, 'chorus')).toEqual({ text: 'abc\n[chorus]\n', caret: 13 });
    expect(insertSection('abc\n', 4, 4, 'bridge').text).toBe('abc\n[bridge]\n');
    // A selection is replaced.
    expect(insertSection('one two', 4, 7, 'verse').text).toBe('one \n[verse]\n');
  });
});

describe('format', () => {
  it('formats sizes, durations and file names', () => {
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(1536)).toBe('1.5 KB');
    expect(formatBytes(1536, 'es')).toBe('1,5 KB');
    expect(formatDuration(65)).toBe('1:05');
    expect(fileNameOf('¡Canción número 1!', 'id1', 'audio/mpeg')).toBe('cancion-numero-1.mp3');
    expect(fileNameOf('***', 'id1', 'audio/wav')).toBe('id1.wav');
  });
});

describe('strings and codes', () => {
  it('has every key in both languages, with the same placeholders', () => {
    for (const table of [MUSIC_STRINGS, WORKLOAD_STRINGS] as { en: Record<string, string>; es: Record<string, string> }[]) {
      expect(Object.keys(table.es).sort()).toEqual(Object.keys(table.en).sort());
      for (const k of Object.keys(table.en)) {
        const ph = (s: string | undefined) => ((s ?? "").match(/\{\w+\}/g) ?? []).sort();
        expect(ph(table.es[k]), k).toEqual(ph(table.en[k]));
        expect(table.es[k]?.length, k).toBeGreaterThan(0);
      }
    }
  });

  it('knows a meaning and a fix in both languages for every code it can show', () => {
    for (const c of ['LAI-SVC-150', 'LAI-SVC-151', 'LAI-SVC-152', 'LAI-SVC-153', 'LAI-SVC-154', 'LAI-SVC-155', 'LAI-SVC-158', 'LAI-LLM-050', 'LAI-LLM-051', 'LAI-LLM-052', 'LAI-LLM-053'])
      expect(KNOWN_CODES).toContain(c);
    for (const c of KNOWN_CODES) {
      for (const l of ['en', 'es'] as const) {
        expect(codeMeaning(c, l), `${c} ${l}`).not.toBe('');
        expect(codeFix(c, l), `${c} ${l}`).not.toBe('');
      }
    }
  });

  it('gives an error without a doctor code the code of the action', () => {
    expect(toErrorInfo(new Error('x'), 'LAI-SVC-152')).toMatchObject({ code: 'LAI-SVC-152', message: 'x' });
    expect(toErrorInfo({ code: 'nope' }, 'LAI-SVC-152').code).toBe('LAI-SVC-152');
    expect(toErrorInfo({ code: 'LAI-SVC-153', fix: 'Wait.' }, 'LAI-SVC-152')).toMatchObject({ code: 'LAI-SVC-153', fix: 'Wait.' });
  });
});
