import { act, fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { LangProvider } from '../lang';
import { FakeMusic, err } from '../test/fakes';
import { MusicStudio } from './MusicStudio';

function wav(name = 'sample.wav', size = 1000, type = 'audio/wav'): File {
  const f = new File([new Uint8Array(4)], name, { type });
  Object.defineProperty(f, 'size', { value: size });
  return f;
}

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

async function startSong(tr: FakeMusic, opId = 'op-1') {
  fill('Lyrics', 'Hello sun');
  fireEvent.click(screen.getByRole('button', { name: 'Generate' }));
  await act(async () => tr.generateResult?.resolve({ op_id: opId, song_ids: ['s1'] }));
}

describe('MusicStudio', () => {
  it('renders the form with the plain fields first and the rest under More options', () => {
    render(<MusicStudio transport={new FakeMusic()} />);
    expect(screen.getByRole('heading', { name: 'Make a song' })).toBeTruthy();
    expect(screen.getByLabelText('Lyrics').tagName).toBe('TEXTAREA');
    expect(screen.getByLabelText('Style')).toBeTruthy();
    expect((screen.getByLabelText('Length in seconds') as HTMLInputElement).value).toBe('60');
    expect((screen.getByLabelText('Language of the singing') as HTMLSelectElement).value).toBe('en');
    expect(screen.getByLabelText('Sample song (optional)')).toBeTruthy();
    expect(screen.getByText('More options').tagName).toBe('SUMMARY');
    // The sample choice appears only once there is a sample.
    expect(screen.queryByText('Sounds like this')).toBeNull();
  });

  it('inserts the section tags on their own lines at the caret', () => {
    render(<MusicStudio transport={new FakeMusic()} />);
    const ta = screen.getByLabelText('Lyrics') as HTMLTextAreaElement;
    fireEvent.click(screen.getByRole('button', { name: 'Add [verse]' }));
    expect(ta.value).toBe('[verse]\n');
    fireEvent.change(ta, { target: { value: '[verse]\nla la' } });
    ta.setSelectionRange(ta.value.length, ta.value.length);
    fireEvent.click(screen.getByRole('button', { name: 'Add [chorus]' }));
    expect(ta.value).toBe('[verse]\nla la\n[chorus]\n');
    fireEvent.click(screen.getByRole('button', { name: 'Add [bridge]' }));
    expect(ta.value).toContain('[bridge]');
  });

  it('checks the form before sending: the fields are marked, the code is shown and nothing is sent', () => {
    const tr = new FakeMusic();
    render(<MusicStudio transport={tr} />);
    fill('Length in seconds', '5');
    fireEvent.click(screen.getByRole('button', { name: 'Generate' }));
    expect(tr.generated).toHaveLength(0);
    const alert = screen.getByRole('alert');
    expect(alert.getAttribute('data-code')).toBe('LAI-SVC-154');
    expect(alert.textContent).toContain('Please fix the fields marked below.');
    expect(screen.getByText('Write some lyrics or a style.')).toBeTruthy();
    expect(screen.getByText('Choose a length from 10 to 240 seconds.')).toBeTruthy();
    const lyrics = screen.getByLabelText('Lyrics');
    expect(lyrics.getAttribute('aria-invalid')).toBe('true');
    // The keyboard lands on the first problem.
    expect(document.activeElement).toBe(lyrics);
  });

  it('rejects a sample of the wrong type or size', () => {
    const tr = new FakeMusic();
    render(<MusicStudio transport={tr} />);
    fill('Lyrics', 'Hello');
    fireEvent.change(screen.getByLabelText('Sample song (optional)'), { target: { files: [wav('notes.txt', 10, 'text/plain')] } });
    fireEvent.click(screen.getByRole('button', { name: 'Generate' }));
    expect(screen.getByText('The sample must be a wav, mp3, flac or ogg file.')).toBeTruthy();
    fireEvent.change(screen.getByLabelText('Sample song (optional)'), { target: { files: [wav('big.wav', 21 * 1024 * 1024)] } });
    fireEvent.click(screen.getByRole('button', { name: 'Generate' }));
    expect(screen.getByText('The sample is too big (at most 20 MB).')).toBeTruthy();
    expect(tr.generated).toHaveLength(0);
  });

  it('offers the plain sample choice, defaulting to "Sounds like this" at strength 0.5', async () => {
    const tr = new FakeMusic();
    render(<MusicStudio transport={tr} />);
    fill('Lyrics', 'My words');
    fireEvent.change(screen.getByLabelText('Sample song (optional)'), { target: { files: [wav()] } });
    expect(screen.getByText(/Chosen: sample.wav/)).toBeTruthy();
    const soundsLike = screen.getByLabelText('Sounds like this') as HTMLInputElement;
    const sameSong = screen.getByLabelText('Same song, my lyrics') as HTMLInputElement;
    expect(soundsLike.checked).toBe(true);
    expect(screen.getByText('How closely to follow the sample: 50 %')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Generate' }));
    expect(tr.generated[0]).toMatchObject({ sample_mode: 'sounds_like', strength: 0.5, lyrics: 'My words', duration_s: 60, variations: 1, seed: null });
    expect(tr.generated[0]?.sample?.name).toBe('sample.wav');
    await act(async () => tr.generateResult?.reject(err('LAI-SVC-153')));

    // "Same song, my lyrics" takes its own default strength (1.0).
    fireEvent.click(sameSong);
    expect(sameSong.checked).toBe(true);
    expect(screen.getByText('How closely to follow the sample: 100 %')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Generate' }));
    expect(tr.generated[1]).toMatchObject({ sample_mode: 'same_song', strength: 1 });
  });

  it('sends the More options and no sample fields without a sample', async () => {
    const tr = new FakeMusic();
    render(<MusicStudio transport={tr} />);
    fill('Style', ' rock, loud ');
    fill('Seed (optional)', '42');
    fill('How many versions', '3');
    fill('Language of the singing', 'es');
    expect((screen.getByLabelText(/How closely to follow the sample/) as HTMLInputElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole('button', { name: 'Generate' }));
    expect(tr.generated[0]).toMatchObject({ tags: 'rock, loud', seed: 42, variations: 3, language: 'es', sample: null, sample_mode: null, strength: null });
  });

  it('shows the progress: sending, waiting, a percentage, then done', async () => {
    const tr = new FakeMusic();
    const created: string[][] = [];
    render(<MusicStudio transport={tr} onSongsCreated={(ids) => created.push(ids)} />);
    fill('Lyrics', 'Hello sun');
    fireEvent.click(screen.getByRole('button', { name: 'Generate' }));
    expect(screen.getByText('Sending…')).toBeTruthy();
    expect((screen.getByRole('button', { name: 'Generate' }) as HTMLButtonElement).disabled).toBe(true);
    await act(async () => tr.generateResult?.resolve({ op_id: 'op-1', song_ids: ['s1'] }));
    expect(screen.getByText('Waiting for its turn…')).toBeTruthy();
    expect(tr.watchers.has('op-1')).toBe(true);
    act(() => tr.emit('op-1', { state: 'running', progress: 0.42 }));
    expect(screen.getByText('Making your song… 42 %')).toBeTruthy();
    expect(document.querySelector('progress')?.getAttribute('value')).toBe('42');
    act(() => tr.emit('op-1', { state: 'done', progress: 1, song_ids: ['s1'] }));
    expect(screen.getByText('Your song is ready. It is in the list of songs.')).toBeTruthy();
    expect(created).toEqual([['s1']]);
    expect(tr.stopped).toContain('op-1');
    expect((screen.getByRole('button', { name: 'Generate' }) as HTMLButtonElement).disabled).toBe(false);
  });

  it('cancels: the button asks the node, and the end says the song was cancelled (LAI-SVC-158)', async () => {
    const tr = new FakeMusic();
    render(<MusicStudio transport={tr} />);
    await startSong(tr);
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(tr.cancelled).toEqual(['op-1']);
    expect((screen.getByRole('button', { name: 'Cancelling…' }) as HTMLButtonElement).disabled).toBe(true);
    await act(async () => tr.emit('op-1', { state: 'cancelled' }));
    const info = document.querySelector('[data-code="LAI-SVC-158"]');
    expect(info?.getAttribute('role')).toBe('status');
    expect(info?.textContent).toContain('The song was cancelled.');
    expect(info?.textContent).toContain('Code: LAI-SVC-158');
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull();
  });

  it('shows a refusal of the node with its code and fix (queue full)', async () => {
    const tr = new FakeMusic();
    render(<MusicStudio transport={tr} />);
    fill('Lyrics', 'Hello');
    fireEvent.click(screen.getByRole('button', { name: 'Generate' }));
    await act(async () => tr.generateResult?.reject(err('LAI-SVC-153', 'The music queue is full.', '', 429)));
    const alert = screen.getByRole('alert');
    expect(alert.getAttribute('data-code')).toBe('LAI-SVC-153');
    expect(alert.textContent).toContain('The music queue is full.');
    expect(alert.textContent).toContain('Wait for a song to finish, then press Generate again.');
  });

  it('shows a song that failed while running with its code (the engine crash code by default)', async () => {
    const tr = new FakeMusic();
    render(<MusicStudio transport={tr} />);
    await startSong(tr);
    act(() => tr.emit('op-1', { state: 'failed', error: { code: 'LAI-SVC-155', message: 'Disk full.' } }));
    expect(screen.getByRole('alert').getAttribute('data-code')).toBe('LAI-SVC-155');

    await startSong(tr, 'op-2');
    act(() => tr.emit('op-2', { state: 'failed', error: null }));
    expect(screen.getByRole('alert').getAttribute('data-code')).toBe('LAI-SVC-152');
  });

  it('keeps Generate off while music is not ready, and says why', () => {
    render(<MusicStudio transport={new FakeMusic()} disabled />);
    const gen = screen.getByRole('button', { name: 'Generate' }) as HTMLButtonElement;
    expect(gen.disabled).toBe(true);
    expect(screen.getByText('Music must be ready before you can generate.')).toBeTruthy();
  });

  it('speaks Spanish, including the sample choice', () => {
    render(
      <LangProvider lang="es">
        <MusicStudio transport={new FakeMusic()} defaultLanguage="es" />
      </LangProvider>,
    );
    expect(screen.getByRole('heading', { name: 'Hacer una canción' })).toBeTruthy();
    expect((screen.getByLabelText('Idioma del canto') as HTMLSelectElement).value).toBe('es');
    fireEvent.change(screen.getByLabelText('Canción de muestra (opcional)'), { target: { files: [wav()] } });
    expect(screen.getByLabelText('Que suene así')).toBeTruthy();
    expect(screen.getByLabelText('La misma canción, con mi letra')).toBeTruthy();
    expect(screen.getByText('Más opciones')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Generar' }));
    expect(screen.getByRole('alert').textContent).toContain('Código: LAI-SVC-154');
    expect(screen.getByText('Escriba una letra o un estilo.')).toBeTruthy();
  });
});
