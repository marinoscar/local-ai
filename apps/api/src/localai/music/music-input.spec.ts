// The form's fields and its cross-field rules, the sample's type from its bytes, the generated title (#568) and the
// strength default (E11 contract 3, 6).
import { MusicRequestFieldsSchema, extensionOf, generatedTitle, musicSettingsOf, sniffAudio, strengthFor, tidyTitle, titleCase } from './music-input';

const bytes = (...parts: Array<string | number[]>) =>
  Uint8Array.from(parts.flatMap((part) => (typeof part === 'string' ? [...part].map((char) => char.charCodeAt(0)) : part)));

describe('MusicRequestFieldsSchema', () => {
  const base = { nodeId: 'ai-n2', lyrics: '[verse]\nPerson A sings' };

  it('applies the defaults: 60 s, en, one variation, the English page, keep the sample', () => {
    expect(MusicRequestFieldsSchema.parse(base)).toEqual({
      nodeId: 'ai-n2',
      lyrics: '[verse]\nPerson A sings',
      ui_language: 'en',
      remove_sample: false,
      duration_s: 60,
      language: 'en',
      variations: 1,
    });
  });

  it('reads the numbers from text fields and checks their ranges (the seed is the contract\'s 31 bits)', () => {
    const parsed = MusicRequestFieldsSchema.parse({ ...base, duration_s: '240', variations: '4', seed: '42', strength: '0.25', sample_mode: 'same_song' });
    expect(parsed).toMatchObject({ duration_s: 240, variations: 4, seed: 42, strength: 0.25, sample_mode: 'same_song' });
    for (const bad of [{ duration_s: '9' }, { duration_s: '241' }, { duration_s: '60.5' }, { variations: '0' }, { variations: '5' }, { strength: '1.5' }, { seed: '-1' }, { seed: 'abc' }, { seed: '2147483648' }]) {
      expect(MusicRequestFieldsSchema.safeParse({ ...base, ...bad }).success).toBe(false);
    }
  });

  it('caps lyrics at 8 KiB and tags at 512 bytes, in UTF-8 bytes', () => {
    expect(MusicRequestFieldsSchema.safeParse({ ...base, lyrics: 'a'.repeat(8192) }).success).toBe(true);
    expect(MusicRequestFieldsSchema.safeParse({ ...base, lyrics: 'a'.repeat(8193) }).success).toBe(false);
    expect(MusicRequestFieldsSchema.safeParse({ ...base, lyrics: 'é'.repeat(4097) }).success).toBe(false);
    expect(MusicRequestFieldsSchema.safeParse({ ...base, tags: 'x'.repeat(513) }).success).toBe(false);
  });

  it('caps the description at 500 characters AND at the 512 bytes of the command\'s tags', () => {
    expect(MusicRequestFieldsSchema.safeParse({ ...base, description: 'x'.repeat(500) }).success).toBe(true);
    expect(MusicRequestFieldsSchema.safeParse({ ...base, description: 'x'.repeat(501) }).success).toBe(false);
    // 250 two-byte letters: 250 characters, 500 bytes: fine. 300 of them: 300 characters but 600 bytes: refused.
    expect(MusicRequestFieldsSchema.safeParse({ ...base, description: 'ñ'.repeat(250) }).success).toBe(true);
    expect(MusicRequestFieldsSchema.safeParse({ ...base, description: 'ñ'.repeat(300) }).success).toBe(false);
  });

  it('takes a title of at most 80 characters without control characters, trimmed', () => {
    expect(MusicRequestFieldsSchema.parse({ ...base, title: '  River Song  ' }).title).toBe('River Song');
    expect(MusicRequestFieldsSchema.safeParse({ ...base, title: 'é'.repeat(80) }).success).toBe(true);
    expect(MusicRequestFieldsSchema.safeParse({ ...base, title: 'x'.repeat(81) }).success).toBe(false);
    expect(MusicRequestFieldsSchema.safeParse({ ...base, title: 'two\nlines' }).success).toBe(false);
  });

  it('refuses an unknown field, a bad node id, language, lyrics mode or prompt id', () => {
    expect(MusicRequestFieldsSchema.safeParse({ ...base, model: 'other' }).success).toBe(false);
    expect(MusicRequestFieldsSchema.safeParse({ ...base, nodeId: '../etc' }).success).toBe(false);
    expect(MusicRequestFieldsSchema.safeParse({ ...base, language: 'Spanish' }).success).toBe(false);
    expect(MusicRequestFieldsSchema.safeParse({ ...base, lyrics_mode: 'karaoke' }).success).toBe(false);
    expect(MusicRequestFieldsSchema.safeParse({ ...base, promptId: 'p1' }).success).toBe(false);
  });
});

describe('musicSettingsOf: the cross-field rules (#568)', () => {
  it('with a lyrics mode: a description is required, and "mine" and "ai" need lyrics', () => {
    expect(musicSettingsOf({ lyrics: 'la la', lyrics_mode: 'mine' })).toBe('description_required');
    expect(musicSettingsOf({ lyrics: ' ', description: 'a slow ballad', lyrics_mode: 'mine' })).toBe('lyrics_required');
    expect(musicSettingsOf({ lyrics: '', description: 'a slow ballad', lyrics_mode: 'ai' })).toBe('lyrics_required');
    expect(musicSettingsOf({ lyrics: 'la la', description: 'a slow ballad', lyrics_mode: 'mine' })).toMatchObject({ lyricsMode: 'mine', lyricsSent: 'la la', description: 'a slow ballad', title: 'La La' });
  });

  it('"No vocals" sends [Instrumental] and keeps the typed lyrics for the prompt; the title comes from the description', () => {
    expect(musicSettingsOf({ lyrics: 'kept for later', description: 'calm piano by the sea, soft rain', lyrics_mode: 'instrumental' })).toEqual({
      title: 'Calm Piano By The Sea',
      description: 'calm piano by the sea, soft rain',
      lyricsMode: 'instrumental',
      lyricsStored: 'kept for later',
      lyricsSent: '[Instrumental]',
      autoLyrics: false,
    });
  });

  it('Automatic (#570): lyrics in the box are sent as written; empty lyrics are marked for the AI, provisionally no vocals', () => {
    expect(musicSettingsOf({ lyrics: 'la la', description: 'a slow ballad', lyrics_mode: 'auto' })).toMatchObject({ lyricsMode: 'auto', lyricsSent: 'la la', lyricsStored: 'la la', autoLyrics: false });
    expect(musicSettingsOf({ lyrics: '  ', description: 'calm piano, soft rain', lyrics_mode: 'auto' })).toEqual({
      title: 'Calm Piano',
      description: 'calm piano, soft rain',
      lyricsMode: 'auto',
      lyricsStored: '  ',
      lyricsSent: '[Instrumental]',
      autoLyrics: true,
    });
    expect(musicSettingsOf({ lyrics: '', lyrics_mode: 'auto' })).toBe('description_required');
  });

  it('an older client (tags, no mode) keeps the old rule: lyrics or tags, the mode derived, lyrics sent as written', () => {
    expect(musicSettingsOf({ lyrics: ' ', tags: '' })).toBe('lyrics_or_tags_required');
    expect(musicSettingsOf({ lyrics: '', tags: 'ambient piano' })).toMatchObject({ lyricsMode: 'instrumental', lyricsSent: '', description: 'ambient piano' });
    expect(musicSettingsOf({ lyrics: 'Person A sings', tags: '' })).toMatchObject({ lyricsMode: 'mine', lyricsSent: 'Person A sings' });
    expect(musicSettingsOf({ lyrics: 'x', tags: 'one', description: 'two' })).toBe('description_and_tags');
  });

  it('a typed title wins; an empty one is generated', () => {
    expect(musicSettingsOf({ lyrics: 'la', description: 'd', lyrics_mode: 'mine', title: 'My Title' })).toMatchObject({ title: 'My Title' });
    expect(musicSettingsOf({ lyrics: '', description: '', lyrics_mode: 'instrumental' })).toBe('description_required');
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

describe('generatedTitle (#568)', () => {
  it('is the first lyric line that is not a section tag, trimmed and title-cased', () => {
    expect(generatedTitle('[verse]\n\n  walking to the river  \n[chorus] la la', 'folk')).toBe('Walking To The River');
    expect(generatedTitle('[intro] morning light', '')).toBe('Morning Light');
    expect(generatedTitle('the DJ plays my iPhone song', '')).toBe('The DJ Plays My IPhone Song');
    expect(generatedTitle('¿dónde está la luna?', '')).toBe('¿Dónde Está La Luna?');
  });

  it('falls back to the first words of the description, then to "Untitled song" by the page\'s language', () => {
    expect(generatedTitle('[instrumental]', 'ambient piano, slow, rain')).toBe('Ambient Piano');
    expect(generatedTitle('', 'a slow spanish ballad about the sea and the old harbour at night. soft piano')).toBe('A Slow Spanish Ballad About The Sea And The Old');
    expect(generatedTitle('', '')).toBe('Untitled song');
    expect(generatedTitle('', '   ', 'es')).toBe('Canción sin título');
  });

  it('is at most 80 characters (counting characters) and removes control characters', () => {
    const long = generatedTitle('x'.repeat(300), '');
    expect([...long]).toHaveLength(80);
    expect(long.endsWith('…')).toBe(true);
    expect([...generatedTitle('ñ'.repeat(300), '')]).toHaveLength(80);
    expect(generatedTitle('Line\u0007 one\u0000', '')).toBe('Line One');
  });

  it('is deterministic', () => {
    expect(generatedTitle('a b c', 'd')).toBe(generatedTitle('a b c', 'd'));
  });
});

describe('tidyTitle and titleCase', () => {
  it('makes a model\'s answer one clean title line', () => {
    expect(tidyTitle('"Harbour Lights"\n\nThis title evokes...')).toBe('Harbour Lights');
    expect(tidyTitle('  **Luz del puerto**.  ')).toBe('Luz del puerto');
    expect(tidyTitle('')).toBe('');
    expect([...tidyTitle('y'.repeat(200))]).toHaveLength(80);
  });

  it('capitalises each word and keeps the rest as written', () => {
    expect(titleCase('rain on the roof')).toBe('Rain On The Roof');
    expect(titleCase('NASA (and me)')).toBe('NASA (And Me)');
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
