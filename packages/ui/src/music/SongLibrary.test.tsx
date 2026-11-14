import { act, fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { LangProvider } from '../lang';
import { FakeMusic, err, song } from '../test/fakes';
import { SongLibrary } from './SongLibrary';

describe('SongLibrary', () => {
  it('lists songs with a player and a download link', async () => {
    const tr = new FakeMusic();
    tr.songs = [song(), song({ id: 'song-2', title: 'Río de noche', origin: 'cloud', tags: '' })];
    render(<SongLibrary transport={tr} timeZone="UTC" />);
    expect(screen.getByText('Loading your songs…')).toBeTruthy();
    expect(await screen.findByRole('heading', { name: 'Morning light' })).toBeTruthy();
    const audio = document.querySelector('audio[aria-label="Play Morning light"]') as HTMLAudioElement;
    expect(audio.getAttribute('src')).toBe('/api/v1/music/songs/song-1/audio');
    expect(audio.hasAttribute('controls')).toBe(true);
    const dl = screen.getByRole('link', { name: 'Download Morning light' });
    expect(dl.getAttribute('href')).toBe('/api/v1/music/songs/song-1/audio');
    expect(dl.getAttribute('download')).toBe('morning-light.wav');
    expect(screen.getByText(/1:05 · .*acoustic, calm/)).toBeTruthy();
    expect(screen.getByText(/Made from the website/)).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Download Río de noche' }).getAttribute('download')).toBe('rio-de-noche.wav');
  });

  it('accepts an audio URL that comes later (a signed URL from the cloud)', async () => {
    const tr = new FakeMusic();
    tr.songs = [song()];
    tr.audioUrl = (() => Promise.resolve('https://storage.example.test/signed')) as unknown as FakeMusic['audioUrl'];
    render(<SongLibrary transport={tr} />);
    expect((await screen.findByRole('link', { name: 'Download Morning light' })).getAttribute('href')).toBe('https://storage.example.test/signed');
  });

  it('says so when there are no songs yet', async () => {
    render(<SongLibrary transport={new FakeMusic()} />);
    expect(await screen.findByText('No songs yet. Make one above.')).toBeTruthy();
  });

  it('asks before deleting, focuses "No, keep it", and keeps the song on No', async () => {
    const tr = new FakeMusic();
    tr.songs = [song()];
    render(<SongLibrary transport={tr} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Delete Morning light' }));
    const dialog = screen.getByRole('alertdialog');
    expect(dialog.textContent).toContain('Delete "Morning light"? This cannot be undone.');
    const keep = screen.getByRole('button', { name: 'No, keep it' });
    expect(document.activeElement).toBe(keep);
    fireEvent.click(keep);
    expect(screen.queryByRole('alertdialog')).toBeNull();
    expect(tr.deleted).toEqual([]);
    expect(screen.getByRole('heading', { name: 'Morning light' })).toBeTruthy();
  });

  it('deletes on Yes and says it was deleted', async () => {
    const tr = new FakeMusic();
    tr.songs = [song(), song({ id: 'song-2', title: 'Second' })];
    render(<SongLibrary transport={tr} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Delete Morning light' }));
    fireEvent.click(screen.getByRole('button', { name: 'Yes, delete it' }));
    expect(tr.deleted).toEqual(['song-1']);
    expect((screen.getByRole('button', { name: 'Deleting…' }) as HTMLButtonElement).disabled).toBe(true);
    await act(async () => tr.deleteResult?.resolve());
    expect(screen.queryByRole('heading', { name: 'Morning light' })).toBeNull();
    expect(screen.getByRole('heading', { name: 'Second' })).toBeTruthy();
    expect(screen.getByText('The song was deleted.')).toBeTruthy();
    expect(document.activeElement).toBe(screen.getByRole('heading', { name: 'Your songs' }));
  });

  it('shows a refused delete with its code (the admin PIN) and keeps the song', async () => {
    const tr = new FakeMusic();
    tr.songs = [song()];
    render(<SongLibrary transport={tr} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Delete Morning light' }));
    fireEvent.click(screen.getByRole('button', { name: 'Yes, delete it' }));
    await act(async () => tr.deleteResult?.reject(err('LAI-SEC-002', '', '', 403)));
    const alert = screen.getByRole('alert');
    expect(alert.getAttribute('data-code')).toBe('LAI-SEC-002');
    expect(alert.textContent).toContain('Enter the admin PIN, then try again.');
    expect(screen.getByRole('heading', { name: 'Morning light' })).toBeTruthy();
  });

  it('pages with "Show more songs"', async () => {
    const tr = new FakeMusic();
    tr.pages = [
      { songs: [song()], next_cursor: '1' },
      { songs: [song({ id: 'song-2', title: 'Second' })], next_cursor: null },
    ];
    render(<SongLibrary transport={tr} pageSize={1} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Show more songs' }));
    expect(await screen.findByRole('heading', { name: 'Second' })).toBeTruthy();
    expect(tr.listCalls[1]).toEqual({ cursor: '1', limit: 1 });
    expect(screen.queryByRole('button', { name: 'Show more songs' })).toBeNull();
  });

  it('reads the list again when refreshKey changes', async () => {
    const tr = new FakeMusic();
    const { rerender } = render(<SongLibrary transport={tr} refreshKey={0} />);
    await screen.findByText('No songs yet. Make one above.');
    tr.songs = [song()];
    rerender(<SongLibrary transport={tr} refreshKey={1} />);
    expect(await screen.findByRole('heading', { name: 'Morning light' })).toBeTruthy();
  });

  it('shows a list failure with a code and a retry', async () => {
    const tr = new FakeMusic();
    tr.listError = new TypeError('Failed to fetch');
    render(<SongLibrary transport={tr} />);
    expect((await screen.findByRole('alert')).getAttribute('data-code')).toBe('LAI-SVC-116');
    expect(screen.getByText('Could not read the list of songs.')).toBeTruthy();
    tr.listError = null;
    tr.songs = [song()];
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByRole('heading', { name: 'Morning light' })).toBeTruthy();
  });

  it('speaks Spanish', async () => {
    const tr = new FakeMusic();
    tr.songs = [song()];
    render(
      <LangProvider lang="es">
        <SongLibrary transport={tr} />
      </LangProvider>,
    );
    expect(screen.getByRole('heading', { name: 'Sus canciones' })).toBeTruthy();
    fireEvent.click(await screen.findByRole('button', { name: 'Borrar Morning light' }));
    expect(screen.getByRole('alertdialog').textContent).toContain('¿Borrar «Morning light»? No se puede deshacer.');
    expect(screen.getByRole('button', { name: 'Sí, borrarla' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'No, conservarla' })).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Descargar Morning light' })).toBeTruthy();
  });
});
