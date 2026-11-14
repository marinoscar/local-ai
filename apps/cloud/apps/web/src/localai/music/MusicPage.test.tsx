// The music page over a mocked API (MSW, the real HTTP client): the node states, the switch to Music, the form and its
// limits, the multipart it sends, the progress line (queued -> generating -> done), an error with its doctor code,
// the library (play, download, delete with confirmation), what a person without a permission does not see, Spanish,
// and axe.
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { http, HttpResponse } from 'msw';
import { setupServer } from 'msw/node';
import { MemoryRouter } from 'react-router-dom';

import { runAxe } from '../../__tests__/axe';
import { LanguageProvider, type Lang } from '../../i18n/LanguageProvider';
import { MusicPage } from './MusicPage';
import { librarySong, musicNode, musicRequest, page, workloadCommand } from './musicFixture';
import type { LibrarySong, MusicNode, MusicRequest } from './musicApi';

let permissions: string[] = [];
vi.mock('@marinoscar/platform-web/identity/headless', () => ({
  usePermissions: () => ({ hasPermission: (permission: string) => permissions.includes(permission) }),
}));

const USER = ['localai_music:use', 'localai_workloads:write'];
const ADMIN = [...USER, 'localai_music:delete'];

let nodes: MusicNode[] = [];
let songs: LibrarySong[] = [];
let active: MusicRequest[] = [];
let requestReads: MusicRequest[] = [];
let posted: FormData | null = null;
let postAnswer: () => Response = () => HttpResponse.json({ data: musicRequest() }, { status: 201 });
let deleted: Array<{ id: string; body: unknown }> = [];
let commandReads: unknown[] = [];

const server = setupServer(
  http.get('/api/localai/music/nodes', () => HttpResponse.json({ data: nodes })),
  http.get('/api/localai/music/songs', () => HttpResponse.json({ data: page(songs) })),
  http.get('/api/localai/music/requests', () => HttpResponse.json({ data: page(active) })),
  http.post('/api/localai/music/requests', async ({ request }) => {
    posted = await request.formData();
    return postAnswer();
  }),
  http.get('/api/localai/music/requests/:id', () => HttpResponse.json({ data: requestReads.length > 1 ? requestReads.shift() : requestReads[0] })),
  http.post('/api/localai/music/requests/:id/cancel', () => HttpResponse.json({ data: musicRequest({ status: 'cancelled', stage: 'cancelled' }) })),
  http.get('/api/localai/music/songs/:id/audio', ({ request, params }) => {
    const download = new URL(request.url).searchParams.get('download') === '1';
    return HttpResponse.json({ data: { url: `https://storage.example.test/b/${String(params.id)}?download=${download}`, expiresAt: '2026-11-14T12:05:00.000Z' } });
  }),
  http.delete('/api/localai/music/songs/:id', async ({ request, params }) => {
    deleted.push({ id: String(params.id), body: await request.json() });
    songs = songs.filter((song) => song.id !== params.id);
    return HttpResponse.json({ data: { id: params.id, deleted: true } });
  }),
  http.post('/api/localai/nodes/:nodeId/workloads/:name/activate', () => HttpResponse.json({ data: workloadCommand() }, { status: 201 })),
  http.get('/api/localai/commands/:id', () => HttpResponse.json({ data: commandReads.length > 1 ? commandReads.shift() : commandReads[0] })),
);

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterAll(() => server.close());
beforeEach(() => {
  permissions = USER;
  nodes = [musicNode()];
  songs = [];
  active = [];
  requestReads = [];
  posted = null;
  deleted = [];
  commandReads = [];
  postAnswer = () => HttpResponse.json({ data: musicRequest() }, { status: 201 });
});
afterEach(() => {
  cleanup();
  server.resetHandlers();
});

function renderPage(lang: Lang = 'en') {
  return render(
    <MemoryRouter>
      <LanguageProvider initial={lang}>
        <MusicPage nodesPollMs={60_000} libraryPollMs={60_000} requestPollMs={20} commandPollMs={20} />
      </LanguageProvider>
    </MemoryRouter>,
  );
}

const lyricsBox = () => screen.getByRole('textbox', { name: 'Lyrics' });

describe('the node picker, state first', () => {
  it('shows every computer with its music state in words, and chooses the first ready one', async () => {
    nodes = [
      musicNode({ nodeId: 'ai-n1', nodeName: 'Desk tower', readiness: 'can_load', activeWorkload: 'coding' }),
      musicNode({ nodeId: 'ai-n2', nodeName: 'Studio laptop', readiness: 'ready' }),
      musicNode({ nodeId: 'ai-n3', nodeName: 'Old laptop', readiness: 'too_little_vram' }),
      musicNode({ nodeId: 'ai-n4', nodeName: 'Travel laptop', readiness: 'offline' }),
      musicNode({ nodeId: 'ai-n5', nodeName: 'Small box', readiness: 'no_music' }),
    ];
    renderPage();
    expect(await screen.findByTestId('readiness-ai-n1')).toHaveTextContent('Can load music');
    expect(screen.getByTestId('readiness-ai-n2')).toHaveTextContent('Music ready');
    expect(screen.getByTestId('readiness-ai-n3')).toHaveTextContent('Too little VRAM');
    expect(screen.getByTestId('readiness-ai-n4')).toHaveTextContent('Offline');
    expect(screen.getByTestId('readiness-ai-n5')).toHaveTextContent('No music here');
    expect(screen.getByRole('radio', { name: /Studio laptop/ })).toBeChecked();
    expect(screen.getByTestId('next-action')).toHaveTextContent('Studio laptop is ready. Write your song below.');

    fireEvent.click(screen.getByRole('radio', { name: /Old laptop/ }));
    expect(screen.getByTestId('next-action')).toHaveTextContent('The graphics card of Old laptop is too small for music.');
    expect(screen.getByRole('button', { name: 'Make the song' })).toBeDisabled();
  });

  it('"Switch to Music" queues the workload, follows it to the end and reads the nodes again', async () => {
    nodes = [musicNode({ readiness: 'can_load', activeWorkload: 'coding' })];
    commandReads = [workloadCommand({ status: 'running' }), workloadCommand({ status: 'succeeded', result: { active: 'music' } })];
    renderPage();
    const button = await screen.findByRole('button', { name: 'Switch Studio laptop to Music' });
    expect(screen.getByRole('button', { name: 'Make the song' })).toBeDisabled();
    nodes = [musicNode({ readiness: 'ready' })];
    fireEvent.click(button);
    expect(await screen.findByRole('status')).toHaveTextContent(/Waiting for Studio laptop|Loading music/);
    await waitFor(() => expect(screen.getByTestId('next-action')).toHaveTextContent('Studio laptop is ready.'));
    expect(screen.getByTestId('readiness-ai-n2')).toHaveTextContent('Music ready');
  });

  it('a failed switch shows its doctor code', async () => {
    nodes = [musicNode({ readiness: 'can_load' })];
    commandReads = [workloadCommand({ status: 'failed', error: { code: 'LAI-LLM-052', message: 'Workload activation failed; previous models restored.' } })];
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Switch Studio laptop to Music' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('LAI-LLM-052');
  });

  it('without localai_workloads:write there is no switch button, only who to ask', async () => {
    permissions = ['localai_music:use'];
    nodes = [musicNode({ readiness: 'can_load' })];
    renderPage();
    expect(await screen.findByText('Ask someone who can switch workloads.')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Switch .* to Music/ })).toBeNull();
  });
});

describe('the form', () => {
  it('sends the multipart fields of the contract, with the sample and its mode, and shows the request', async () => {
    requestReads = [musicRequest({ status: 'queued', stage: 'queued' })];
    renderPage();
    await screen.findByTestId('next-action');
    fireEvent.change(lyricsBox(), { target: { value: '[verse]\nWalking to the river' } });
    fireEvent.change(screen.getByRole('textbox', { name: 'Style' }), { target: { value: 'folk' } });
    const file = new File([new Uint8Array([82, 73, 70, 70])], 'hum.wav', { type: 'audio/wav' });
    fireEvent.change(screen.getByLabelText(/Sample \(optional\)/), { target: { files: [file] } });
    fireEvent.click(screen.getByRole('radio', { name: 'Same song, my lyrics' }));
    fireEvent.click(screen.getByRole('button', { name: 'Make the song' }));

    await waitFor(() => expect(posted).not.toBeNull());
    const form = posted!;
    // A form encodes line breaks as CRLF (as a browser does); the API's title reader takes both.
    expect(Object.fromEntries([...form.entries()].filter(([key]) => key !== 'sample'))).toEqual({
      nodeId: 'ai-n2',
      lyrics: '[verse]\r\nWalking to the river',
      tags: 'folk',
      duration_s: '60',
      language: 'en',
      variations: '1',
      sample_mode: 'same_song',
    });
    // The file travels as the `sample` part. jsdom's File is not Node's Blob, so its bytes and name do not survive the
    // test transport; the API's handling of the bytes is proven in its own route and db tests.
    expect(form.has('sample')).toBe(true);
    expect(await screen.findByTestId('request-stage')).toHaveTextContent('Waiting for Studio laptop');
  });

  it('checks the limits before sending: lyrics over 8 KB, and a song needs lyrics or a style', async () => {
    renderPage();
    await screen.findByTestId('next-action');
    const make = screen.getByRole('button', { name: 'Make the song' });
    expect(make).toBeDisabled();
    fireEvent.change(lyricsBox(), { target: { value: 'é'.repeat(4097) } });
    expect(screen.getByText('The lyrics are too long (8 KB at most).')).toBeInTheDocument();
    expect(make).toBeDisabled();
    fireEvent.change(lyricsBox(), { target: { value: 'Short song' } });
    expect(make).toBeEnabled();
    expect(posted).toBeNull();
  });

  it('a refusal shows its doctor code (429 LAI-SVC-153)', async () => {
    postAnswer = () => HttpResponse.json({ statusCode: 429, message: 'You already have songs waiting. Wait for one to finish, then try again.', details: { code: 'LAI-SVC-153' } }, { status: 429 });
    renderPage();
    await screen.findByTestId('next-action');
    fireEvent.change(lyricsBox(), { target: { value: 'A song' } });
    fireEvent.click(screen.getByRole('button', { name: 'Make the song' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/songs waiting.*\(LAI-SVC-153\)/);
  });
});

describe('progress and the library', () => {
  it('walks queued -> generating -> done and then shows the song in the library', async () => {
    requestReads = [
      musicRequest({ status: 'running', stage: 'generating' }),
      musicRequest({ status: 'succeeded', stage: 'done', songs: [{ id: 'a0000000-0000-4000-8000-000000000001', index: 0, status: 'ready', mime: 'audio/wav', sizeBytes: 10, durationS: 60, readyAt: null }] }),
    ];
    active = [musicRequest()];
    renderPage();
    songs = [librarySong({ title: 'Walking to the river' })];
    const seen: string[] = [];
    const stage = await screen.findByTestId('request-stage');
    await waitFor(() => {
      const text = stage.textContent ?? '';
      if (seen.at(-1) !== text) seen.push(text);
      expect(text).toBe('Done');
    });
    // The line passed through the node's work before it was done (the first read may already be past "Waiting").
    expect(seen.some((text) => /Waiting for Studio laptop|Studio laptop is making the song/.test(text))).toBe(true);
    expect(await screen.findByTestId(`song-${songs[0].id}`)).toHaveTextContent('Walking to the river');
  });

  it('a failed request shows the node\'s doctor code and message', async () => {
    active = [musicRequest({ status: 'failed', stage: 'failed', error: { code: 'LAI-SVC-151', message: 'Music is not loaded. Switch to the Music workload.' } })];
    renderPage();
    const item = await screen.findByTestId(`request-${active[0].id}`);
    expect(within(item).getByText(/Music is not loaded.*\(LAI-SVC-151\)/)).toBeInTheDocument();
  });

  it('plays a song from a signed URL and downloads it', async () => {
    songs = [librarySong()];
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Play Morning light' }));
    await waitFor(() => expect(document.querySelector('audio')).toHaveAttribute('src', `https://storage.example.test/b/${songs[0].id}?download=false`));
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined);
    fireEvent.click(screen.getByRole('button', { name: 'Download Morning light' }));
    await waitFor(() => expect(click).toHaveBeenCalled());
    click.mockRestore();
  });

  it('delete asks first, sends the typed confirmation, and the song leaves the library', async () => {
    permissions = ADMIN;
    songs = [librarySong()];
    const id = songs[0].id;
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Delete Morning light' }));
    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent('The copy on Studio laptop stays there.');
    expect(deleted).toEqual([]);
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(deleted).toEqual([{ id, body: { confirm: id } }]));
    await waitFor(() => expect(screen.queryByTestId(`song-${id}`)).toBeNull());
  });

  it('without localai_music:delete there is no Delete button', async () => {
    songs = [librarySong()];
    renderPage();
    await screen.findByRole('button', { name: 'Play Morning light' });
    expect(screen.queryByRole('button', { name: /Delete/ })).toBeNull();
  });
});

describe('language and accessibility', () => {
  it('speaks Spanish', async () => {
    nodes = [musicNode({ readiness: 'can_load' })];
    renderPage('es');
    expect(await screen.findByRole('heading', { level: 1, name: 'Música' })).toBeInTheDocument();
    expect(screen.getByTestId('readiness-ai-n2')).toHaveTextContent('Puede cargar música');
    expect(screen.getByRole('button', { name: 'Cambiar Studio laptop a Música' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Crear la canción' })).toBeDisabled();
  });

  it('has no axe violations', async () => {
    songs = [librarySong()];
    const { container } = renderPage();
    await screen.findByRole('button', { name: 'Play Morning light' });
    let results: Awaited<ReturnType<typeof runAxe>> | undefined;
    await act(async () => {
      results = await runAxe(container);
    });
    expect(results!.violations.map((violation) => violation.id)).toEqual([]);
  });
});
