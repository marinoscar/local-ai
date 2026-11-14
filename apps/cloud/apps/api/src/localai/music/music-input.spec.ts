// The form's fields, the sample's type from its bytes, the library title and the strength default (E11 contract 3, 6).
import { MusicRequestFieldsSchema, extensionOf, sniffAudio, songTitle, strengthFor } from './music-input';

const bytes = (...parts: Array<string | number[]>) =>
  Uint8Array.from(parts.flatMap((part) => (typeof part === 'string' ? [...part].map((char) => char.charCodeAt(0)) : part)));

describe('MusicRequestFieldsSchema', () => {
  const base = { nodeId: 'ai-n2', lyrics: '[verse]\nPerson A sings' };

  it('applies the defaults: 60 s, en, one variation, empty tags', () => {
    expect(MusicRequestFieldsSchema.parse(base)).toEqual({ nodeId: 'ai-n2', lyrics: '[verse]\nPerson A sings', tags: '', duration_s: 60, language: 'en', variations: 1 });
  });

  it('reads the numbers from text fields and checks their ranges', () => {
    const parsed = MusicRequestFieldsSchema.parse({ ...base, duration_s: '240', variations: '4', seed: '42', strength: '0.25', sample_mode: 'same_song' });
    expect(parsed).toMatchObject({ duration_s: 240, variations: 4, seed: 42, strength: 0.25, sample_mode: 'same_song' });
    for (const bad of [{ duration_s: '9' }, { duration_s: '241' }, { duration_s: '60.5' }, { variations: '0' }, { variations: '5' }, { strength: '1.5' }, { seed: '-1' }, { seed: 'abc' }]) {
      expect(MusicRequestFieldsSchema.safeParse({ ...base, ...bad }).success).toBe(false);
    }
  });

  it('caps lyrics at 8 KiB and tags at 512 bytes, in UTF-8 bytes', () => {
    expect(MusicRequestFieldsSchema.safeParse({ ...base, lyrics: 'a'.repeat(8192) }).success).toBe(true);
    expect(MusicRequestFieldsSchema.safeParse({ ...base, lyrics: 'a'.repeat(8193) }).success).toBe(false);
    expect(MusicRequestFieldsSchema.safeParse({ ...base, lyrics: 'é'.repeat(4097) }).success).toBe(false);
    expect(MusicRequestFieldsSchema.safeParse({ ...base, tags: 'x'.repeat(513) }).success).toBe(false);
  });

  it('needs lyrics or a style, refuses an unknown field and a bad node id or language', () => {
    expect(MusicRequestFieldsSchema.safeParse({ nodeId: 'ai-n2' }).success).toBe(false);
    expect(MusicRequestFieldsSchema.safeParse({ nodeId: 'ai-n2', lyrics: '   ', tags: '' }).success).toBe(false);
    expect(MusicRequestFieldsSchema.safeParse({ nodeId: 'ai-n2', tags: 'ambient piano' }).success).toBe(true);
    expect(MusicRequestFieldsSchema.safeParse({ ...base, model: 'other' }).success).toBe(false);
    expect(MusicRequestFieldsSchema.safeParse({ ...base, nodeId: '../etc' }).success).toBe(false);
    expect(MusicRequestFieldsSchema.safeParse({ ...base, language: 'Spanish' }).success).toBe(false);
  });
});

describe('sniffAudio', () => {
  it('knows WAV, FLAC, OGG and MP3 (ID3 tag or frame sync) by their bytes', () => {
    expect(sniffAudio(bytes('RIFF', [0, 0, 0, 0], 'WAVE'))).toEqual({ mime: 'audio/wav', ext: 'wav' });
    expect(sniffAudio(bytes('fLaC'))).toEqual({ mime: 'audio/flac', ext: 'flac' });
    expect(sniffAudio(bytes('OggS'))).toEqual({ mime: 'audio/ogg', ext: 'ogg' });
    expect(sniffAudio(bytes('ID3', [4, 0]))).toEqual({ mime: 'audio/mpeg', ext: 'mp3' });
    expect(sniffAudio(bytes([0xff, 0xfb, 0x90, 0x00]))).toEqual({ mime: 'audio/mpeg', ext: 'mp3' });
  });

  it('refuses anything else, whatever its name says: text, HTML, a video, an empty file', () => {
    expect(sniffAudio(bytes('hello world'))).toBeNull();
    expect(sniffAudio(bytes('<html>'))).toBeNull();
    expect(sniffAudio(bytes([0, 0, 0, 0x18], 'ftypmp42'))).toBeNull();
    expect(sniffAudio(bytes('RIFF', [0, 0, 0, 0], 'AVI '))).toBeNull();
    expect(sniffAudio(new Uint8Array())).toBeNull();
    expect(sniffAudio(bytes([0xff, 0xf9, 0xf0, 0x00]))).toBeNull();
  });
});

describe('songTitle', () => {
  it('is the first lyric line without structure tags', () => {
    expect(songTitle('[verse]\n\n  Walking to the river  \n[chorus] la la', 'folk')).toBe('Walking to the river');
    expect(songTitle('[intro] Morning light', '')).toBe('Morning light');
  });

  it('falls back to the tags, then to "Untitled", and is cut to 120 characters', () => {
    expect(songTitle('[instrumental]', 'ambient, piano')).toBe('ambient, piano');
    expect(songTitle('', '')).toBe('Untitled');
    const long = songTitle('x'.repeat(300), '');
    expect(long).toHaveLength(120);
    expect(long.endsWith('…')).toBe(true);
  });

  it('removes control characters', () => {
    expect(songTitle('Line\u0007 one\u0000', '')).toBe('Line one');
  });
});

describe('strengthFor and extensionOf', () => {
  it('defaults the strength per mode only when there is a sample', () => {
    expect(strengthFor(undefined, undefined, false)).toBeUndefined();
    expect(strengthFor('sounds_like', undefined, true)).toBe(0.5);
    expect(strengthFor('same_song', undefined, true)).toBe(1);
    expect(strengthFor(undefined, undefined, true)).toBe(0.5);
    expect(strengthFor('same_song', 0.3, true)).toBe(0.3);
  });

  it('names a download by its type, wav by default', () => {
    expect(extensionOf('audio/mpeg')).toBe('mp3');
    expect(extensionOf('audio/flac')).toBe('flac');
    expect(extensionOf('audio/ogg')).toBe('ogg');
    expect(extensionOf('audio/wav')).toBe('wav');
    expect(extensionOf(null)).toBe('wav');
  });
});
