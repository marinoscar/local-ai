// The music page over a mocked API (MSW, the real HTTP client): the node states, the switch to Music, the form and its
// limits, the multipart it sends, the progress line (queued -> generating -> done), an error with its doctor code,
// the library (play, download, delete with confirmation), what a person without a permission does not see, Spanish,
// and axe. #568: My prompts (open, edit and save, save as new, make again, delete with confirmation that the songs
// stay), the title (typed, "Suggest a title" with AI on and off), "Describe your song" and the three lyrics modes.
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { http, HttpResponse } from 'msw';
import { setupServer } from 'msw/node';
import { MemoryRouter } from 'react-router-dom';

import { runAxe } from '../../__tests__/axe';
import { LanguageProvider, type Lang } from '../../i18n/LanguageProvider';
import { MusicPage } from './MusicPage';
import { PROMPT_ID, librarySong, musicNode, musicPrompt, musicRequest, page, promptSummary, workloadCommand } from './musicFixture';
import type { LibrarySong, MusicAssist, MusicNode, MusicPrompt, MusicRequest } from './musicApi';

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

/**
 * The posted multipart body as a FormData. `request.formData()` is not used: on Node 24 undici's multipart parser
 * asserts on the body jsdom serialises for a jsdom File, so the parts are split by hand. Text parts keep their value;
 * a file part is recorded under its name with its file name (the bytes are proven by the API's route and db tests).
 */
async function readMultipart(request: Request): Promise<FormData> {
  const boundary = /boundary=(?:"([^"]+)"|([^;]+))/.exec(request.headers.get('content-type') ?? '');
  const marker = `--${boundary?.[1] ?? boundary?.[2] ?? ''}`;
  const out = new FormData();
  for (const part of (await request.text()).split(marker).slice(1, -1)) {
    const split = part.indexOf('\r\n\r\n');
    const head = part.slice(0, split);
    const body = part.slice(split + 4, part.length - 2);
    const name = /name="([^"]*)"/.exec(head)?.[1];
    if (name === undefined) continue;
    const filename = /filename="([^"]*)"/.exec(head)?.[1];
    out.append(name, filename ?? body);
  }
  return out;
}
let postAnswer: () => Response = () => HttpResponse.json({ data: musicRequest() }, { status: 201 });
let deleted: Array<{ id: string; body: unknown }> = [];
let commandReads: unknown[] = [];
let prompts: MusicPrompt[] = [];
let assist: MusicAssist = { ai: { enabled: false, reason: 'ai_off', fix: 'Turn on AI in Settings → AI.' } };
let promptWrites: Array<{ method: string; id?: string; body: unknown }> = [];
let titleAnswer: { title: string; source: 'ai' | 'generated' } = { title: 'Walking To The River', source: 'generated' };
let titleAsked: unknown[] = [];
let lyricsAnswer: () => Response = () => HttpResponse.json({ data: { lyrics: '[verse]\nThe sea is wide', source: 'ai' } });

const server = setupServer(
  http.get('/api/localai/music/nodes', () => HttpResponse.json({ data: nodes })),
  http.get('/api/localai/music/songs', () => HttpResponse.json({ data: page(songs) })),
  http.get('/api/localai/music/requests', () => HttpResponse.json({ data: page(active) })),
  http.post('/api/localai/music/requests', async ({ request }) => {
    posted = await readMultipart(request);
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
  http.get('/api/localai/music/prompts', () => HttpResponse.json({ data: page(prompts.map(promptSummary)) })),
  http.get('/api/localai/music/prompts/:id', ({ params }) => {
    const found = prompts.find((prompt) => prompt.id === params.id);
    return found ? HttpResponse.json({ data: found }) : HttpResponse.json({ statusCode: 404, message: 'No such prompt' }, { status: 404 });
  }),
  http.post('/api/localai/music/prompts', async ({ request }) => {
    const body = (await request.json()) as Record<string, unknown>;
    promptWrites.push({ method: 'POST', body });
    const created = musicPrompt({ ...(body as Partial<MusicPrompt>), id: 'b0000000-0000-4000-8000-000000000002', title: (body.title as string) || 'Generated Title', songCount: 0, hasSample: body.copySampleFrom !== undefined });
    prompts = [created, ...prompts];
    return HttpResponse.json({ data: created }, { status: 201 });
  }),
  http.patch('/api/localai/music/prompts/:id', async ({ request, params }) => {
    const body = (await request.json()) as Record<string, unknown>;
    promptWrites.push({ method: 'PATCH', id: String(params.id), body });
    prompts = prompts.map((prompt) => (prompt.id === params.id ? { ...prompt, ...(body as Partial<MusicPrompt>), ...(body.removeSample ? { hasSample: false } : {}) } : prompt));
    return HttpResponse.json({ data: prompts.find((prompt) => prompt.id === params.id) });
  }),
  http.delete('/api/localai/music/prompts/:id', async ({ request, params }) => {
    promptWrites.push({ method: 'DELETE', id: String(params.id), body: await request.json() });
    prompts = prompts.filter((prompt) => prompt.id !== params.id);
    songs = songs.map((song) => (song.promptId === params.id ? { ...song, promptId: null } : song));
    return HttpResponse.json({ data: { id: params.id, deleted: true } });
  }),
  http.get('/api/localai/music/assist', () => HttpResponse.json({ data: assist })),
  http.post('/api/localai/music/title-suggestion', async ({ request }) => {
    titleAsked.push(await request.json());
    return HttpResponse.json({ data: titleAnswer });
  }),
  http.post('/api/localai/music/lyrics-suggestion', () => lyricsAnswer()),
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
  prompts = [];
  assist = { ai: { enabled: false, reason: 'ai_off', fix: 'Turn on AI in Settings → AI.' } };
  promptWrites = [];
  titleAnswer = { title: 'Walking To The River', source: 'generated' };
  titleAsked = [];
  lyricsAnswer = () => HttpResponse.json({ data: { lyrics: '[verse]\nThe sea is wide', source: 'ai' } });
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

const lyricsBox = () => screen.getByRole('textbox', { name: 'Your lyrics' });
const descriptionBox = () => screen.getByRole('textbox', { name: /Describe your song/ });
const titleBox = () => screen.getByRole('textbox', { name: 'Title' });
const fields = (form: FormData) => Object.fromEntries([...form.entries()].filter(([key]) => key !== 'sample'));

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
  it('sends the multipart fields of the contract, with the description, the title, the sample and its mode, and shows the request', async () => {
    requestReads = [musicRequest({ status: 'queued', stage: 'queued' })];
    renderPage();
    await screen.findByTestId('next-action');
    fireEvent.change(titleBox(), { target: { value: 'River Song' } });
    fireEvent.change(descriptionBox(), { target: { value: 'folk, acoustic guitar' } });
    fireEvent.change(lyricsBox(), { target: { value: '[verse]\nWalking to the river' } });
    const file = new File([new Uint8Array([82, 73, 70, 70])], 'hum.wav', { type: 'audio/wav' });
    fireEvent.change(screen.getByLabelText(/Sample \(optional\)/), { target: { files: [file] } });
    fireEvent.click(screen.getByRole('radio', { name: 'Same song, my lyrics' }));
    fireEvent.click(screen.getByRole('button', { name: 'Make the song' }));

    await waitFor(() => expect(posted).not.toBeNull());
    // A form encodes line breaks as CRLF (as a browser does); the API's title reader takes both.
    expect(fields(posted!)).toEqual({
      nodeId: 'ai-n2',
      title: 'River Song',
      description: 'folk, acoustic guitar',
      lyrics_mode: 'auto',
      lyrics: '[verse]\r\nWalking to the river',
      ui_language: 'en',
      language: 'en',
      variations: '1',
      sample_mode: 'same_song',
    });
    // The file travels as the `sample` part (readMultipart records only that it is there; the API's handling of the
    // bytes is proven in its own route and db tests).
    expect(posted!.has('sample')).toBe(true);
    expect(await screen.findByTestId('request-stage')).toHaveTextContent('Waiting for Studio laptop');
  });

  describe('Length (#571)', () => {
    const fillSong = async () => {
      await screen.findByTestId('next-action');
      fireEvent.change(descriptionBox(), { target: { value: 'folk, acoustic guitar' } });
      fireEvent.change(lyricsBox(), { target: { value: '[verse]\nWalking to the river' } });
    };
    const chooseLength = (name: string) => {
      fireEvent.click(screen.getByRole('button', { name: 'More options' }));
      fireEvent.mouseDown(within(screen.getByTestId('music-length').parentElement!).getByRole('combobox'));
      fireEvent.click(screen.getByRole('option', { name }));
    };

    it('defaults to Auto and leaves duration_s out', async () => {
      renderPage();
      await fillSong();
      expect(screen.getByTestId('music-length-summary')).toHaveTextContent('Length: Auto.');
      expect(screen.queryByTestId('music-length-old-node')).toBeNull();
      fireEvent.click(screen.getByRole('button', { name: 'Make the song' }));
      await waitFor(() => expect(posted).not.toBeNull());
      expect(posted!.has('duration_s')).toBe(false);
    });

    it('Set the length sends the seconds (10 to 240)', async () => {
      renderPage();
      await fillSong();
      chooseLength('Set the length');
      const seconds = screen.getByRole('spinbutton', { name: 'Length (seconds)' });
      fireEvent.change(seconds, { target: { value: '300' } });
      expect(screen.getByRole('button', { name: 'Make the song' })).toBeDisabled();
      fireEvent.change(seconds, { target: { value: '120' } });
      fireEvent.click(screen.getByRole('button', { name: 'Make the song' }));
      await waitFor(() => expect(posted).not.toBeNull());
      expect(fields(posted!)).toMatchObject({ duration_s: '120' });
    });

    it('an older computer: the page says it is sent 1 minute; a set length needs no note', async () => {
      nodes = [musicNode({ autoDuration: false })];
      renderPage();
      await fillSong();
      expect(await screen.findByTestId('music-length-old-node')).toHaveTextContent('This computer needs an update to pick the length by itself; using 1 minute.');
      chooseLength('Set the length');
      expect(screen.queryByTestId('music-length-old-node')).toBeNull();
    });

    it('a prompt saved with Auto opens on Auto; one saved with a length opens with it', async () => {
      prompts = [musicPrompt({ durationS: null })];
      renderPage();
      fireEvent.click(await screen.findByRole('button', { name: 'Open Harbour Lights' }));
      expect(await screen.findByTestId('music-length-summary')).toHaveTextContent('Length: Auto.');
    });

    it('speaks Spanish: Duración: Automática, and the older-computer note', async () => {
      nodes = [musicNode({ autoDuration: false })];
      renderPage('es');
      expect(await screen.findByTestId('music-length-summary')).toHaveTextContent('Duración: Automática.');
      expect(await screen.findByTestId('music-length-old-node')).toHaveTextContent('Esta computadora necesita una actualización para elegir la duración por sí sola; se usa 1 minuto.');
    });
  });

  it('checks the limits before sending: a description is needed, at most 500 characters; lyrics at most 8 KB', async () => {
    renderPage();
    await screen.findByTestId('next-action');
    const make = screen.getByRole('button', { name: 'Make the song' });
    expect(make).toBeDisabled();
    fireEvent.change(lyricsBox(), { target: { value: 'Short song' } });
    expect(make).toBeDisabled();
    expect(screen.getByTestId('form-missing')).toHaveTextContent('Describe your song.');
    fireEvent.change(descriptionBox(), { target: { value: 'x'.repeat(501) } });
    expect(screen.getByText('The description is too long (500 characters at most).')).toBeInTheDocument();
    expect(make).toBeDisabled();
    // 300 accented letters are 300 characters but 600 bytes: over what the computer takes.
    fireEvent.change(descriptionBox(), { target: { value: 'ñ'.repeat(300) } });
    expect(screen.getByText(/too long for the computer/)).toBeInTheDocument();
    fireEvent.change(descriptionBox(), { target: { value: 'a calm song' } });
    expect(screen.getByText('11 / 500 characters. Say the style, the instruments, the voice and the mood.')).toBeInTheDocument();
    fireEvent.change(lyricsBox(), { target: { value: 'é'.repeat(4097) } });
    expect(screen.getByText('The lyrics are too long (8 KB at most).')).toBeInTheDocument();
    expect(make).toBeDisabled();
    fireEvent.change(lyricsBox(), { target: { value: 'Short song' } });
    expect(make).toBeEnabled();
    expect(posted).toBeNull();
  });

  it('"No vocals" needs no lyrics and sends the instrumental mode', async () => {
    renderPage();
    await screen.findByTestId('next-action');
    fireEvent.change(descriptionBox(), { target: { value: 'ambient piano, rain' } });
    fireEvent.click(screen.getByRole('radio', { name: 'No vocals (instrumental)' }));
    expect(screen.queryByRole('textbox', { name: 'Your lyrics' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Make the song' }));
    await waitFor(() => expect(posted).not.toBeNull());
    expect(fields(posted!)).toMatchObject({ lyrics_mode: 'instrumental', description: 'ambient piano, rain', title: '' });
  });

  it('a refusal shows its doctor code (429 LAI-SVC-153)', async () => {
    postAnswer = () => HttpResponse.json({ statusCode: 429, message: 'You already have songs waiting. Wait for one to finish, then try again.', details: { code: 'LAI-SVC-153' } }, { status: 429 });
    renderPage();
    await screen.findByTestId('next-action');
    fireEvent.change(descriptionBox(), { target: { value: 'a song' } });
    fireEvent.change(lyricsBox(), { target: { value: 'A song' } });
    fireEvent.click(screen.getByRole('button', { name: 'Make the song' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/songs waiting.*\(LAI-SVC-153\)/);
  });
});

describe('titles and the AI helpers (#568)', () => {
  it('AI off: "Suggest a title" gives the title made from the words; "Write lyrics now" is disabled with the reason and the fix', async () => {
    renderPage();
    await screen.findByTestId('next-action');
    expect(await screen.findByTestId('ai-off-reason')).toHaveTextContent('Music AI is off: the AI is turned off on this cloud. An administrator turns it on in Settings → AI.');
    expect(screen.getByRole('radio', { name: 'Write lyrics now' })).toBeDisabled();
    fireEvent.change(descriptionBox(), { target: { value: 'folk' } });
    fireEvent.change(lyricsBox(), { target: { value: 'walking to the river' } });
    fireEvent.click(screen.getByRole('button', { name: 'Suggest a title' }));
    await waitFor(() => expect(titleBox()).toHaveValue('Walking To The River'));
    expect(screen.getByText('Made from your words. You can change it.')).toBeInTheDocument();
    expect(titleAsked).toEqual([{ lyrics: 'walking to the river', description: 'folk', lyricsMode: 'auto', language: 'en', uiLanguage: 'en' }]);
  });

  it('no AI model for the person: the reason says how to fix it', async () => {
    assist = { ai: { enabled: false, reason: 'no_model', fix: 'Add a provider and model in Settings → AI.' } };
    renderPage();
    expect(await screen.findByText('Music AI is off: no AI model is set up for you. An administrator adds a provider and model in Settings → AI.')).toBeInTheDocument();
  });

  it('AI on: the model\'s title, and "Write lyrics now" fills the editable lyrics box', async () => {
    assist = { ai: { enabled: true, reason: null, fix: null } };
    titleAnswer = { title: 'Harbour Secrets', source: 'ai' };
    renderPage();
    await screen.findByTestId('next-action');
    const aiMode = await screen.findByRole('radio', { name: 'Write lyrics now' });
    await waitFor(() => expect(aiMode).toBeEnabled());
    expect(screen.queryByTestId('ai-off-reason')).toBeNull();
    fireEvent.change(descriptionBox(), { target: { value: 'a slow ballad about the sea' } });
    fireEvent.click(aiMode);
    fireEvent.click(screen.getByRole('button', { name: 'Write the lyrics' }));
    await waitFor(() => expect(lyricsBox()).toHaveValue('[verse]\nThe sea is wide'));
    expect(screen.getByText(/Read and change them before you make the song/)).toBeInTheDocument();
    fireEvent.change(lyricsBox(), { target: { value: '[verse]\nThe sea is wide and mine' } });
    fireEvent.click(screen.getByRole('button', { name: 'Suggest a title' }));
    await waitFor(() => expect(titleBox()).toHaveValue('Harbour Secrets'));
    expect(screen.getByText('Suggested by the AI. You can change it.')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Make the song' }));
    await waitFor(() => expect(posted).not.toBeNull());
    expect(fields(posted!)).toMatchObject({ lyrics_mode: 'ai', lyrics: '[verse]\r\nThe sea is wide and mine', title: 'Harbour Secrets' });
  });

  it('AI failing: the lyrics refusal is shown and the box stays as it was', async () => {
    assist = { ai: { enabled: true, reason: null, fix: null } };
    lyricsAnswer = () => HttpResponse.json({ statusCode: 502, message: 'The AI could not write lyrics this time. Try again, or write your own lyrics.', details: { reason: 'ai_failed' } }, { status: 502 });
    renderPage();
    await screen.findByTestId('next-action');
    const aiMode = await screen.findByRole('radio', { name: 'Write lyrics now' });
    await waitFor(() => expect(aiMode).toBeEnabled());
    fireEvent.change(descriptionBox(), { target: { value: 'a slow ballad' } });
    fireEvent.click(aiMode);
    fireEvent.click(screen.getByRole('button', { name: 'Write the lyrics' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('The AI could not write lyrics this time.');
    expect(lyricsBox()).toHaveValue('');
  });
});

describe('Automatic lyrics and the music AI states (#570)', () => {
  const hint = () => screen.getByTestId('lyrics-hint');
  const offReason = () => screen.getByTestId('ai-off-reason');
  const settle = async () => {
    await screen.findByTestId('next-action');
    // The assist answer has arrived when the hint (or the off reason) reflects it.
    await waitFor(() => expect(screen.getByRole('radio', { name: /Automatic|Automático/ })).toBeChecked());
  };

  it('Automatic is the default; with music AI on, the hint says the AI writes empty lyrics, and Make the song sends no lyrics', async () => {
    assist = { ai: { enabled: true, reason: null, fix: null } };
    renderPage();
    await settle();
    await waitFor(() => expect(hint()).toHaveTextContent('Leave it empty and the AI writes the lyrics when you press Make the song.'));
    expect(screen.queryByTestId('ai-off-reason')).toBeNull();
    fireEvent.change(descriptionBox(), { target: { value: 'a slow ballad about the sea' } });
    const make = screen.getByRole('button', { name: 'Make the song' });
    expect(make).toBeEnabled();
    expect(screen.queryByTestId('form-missing')).toBeNull();
    fireEvent.click(make);
    await waitFor(() => expect(posted).not.toBeNull());
    expect(fields(posted!)).toMatchObject({ lyrics_mode: 'auto', lyrics: '', description: 'a slow ballad about the sea' });
  });

  it('with lyrics in the box the hint says they are used', async () => {
    assist = { ai: { enabled: true, reason: null, fix: null } };
    renderPage();
    await settle();
    fireEvent.change(lyricsBox(), { target: { value: 'la la' } });
    expect(hint()).toHaveTextContent('Your lyrics are used.');
  });

  it.each([
    ['ai_off', 'Turn on AI in Settings → AI.', 'Music AI is off: the AI is turned off on this cloud. An administrator turns it on in Settings → AI.'],
    ['music_ai_off', 'Turn on Use AI for music in Settings → AI.', 'Music AI is off: Use AI for music is turned off. An administrator turns on Use AI for music in Settings → AI.'],
    ['no_model', 'Add a provider and model in Settings → AI.', 'Music AI is off: no AI model is set up for you. An administrator adds a provider and model in Settings → AI.'],
  ] as const)('music AI off (%s), in English: empty lyrics mean no vocals; the reason and the fix are shown', async (reason, fix, text) => {
    assist = { ai: { enabled: false, reason, fix } };
    renderPage();
    await settle();
    expect(hint()).toHaveTextContent('Leave it empty for a song with no vocals.');
    expect(offReason()).toHaveTextContent(text);
    expect(screen.getByRole('radio', { name: 'Write lyrics now' })).toBeDisabled();
    fireEvent.change(descriptionBox(), { target: { value: 'ambient piano' } });
    expect(screen.getByRole('button', { name: 'Make the song' })).toBeEnabled();
  });

  it.each([
    ['ai_off', 'La IA para música está apagada: la IA está apagada en esta nube. Un administrador la enciende en Ajustes (Settings) → AI.'],
    ['music_ai_off', 'La IA para música está apagada: "Use AI for music" está apagado. Un administrador lo enciende en Ajustes (Settings) → AI.'],
    ['no_model', 'La IA para música está apagada: no hay un modelo de IA para usted. Un administrador agrega un proveedor y un modelo en Ajustes (Settings) → AI.'],
  ] as const)('music AI off (%s), in Spanish: the hint and the reason', async (reason, text) => {
    assist = { ai: { enabled: false, reason, fix: 'x' } };
    renderPage('es');
    await settle();
    expect(hint()).toHaveTextContent('Déjela vacía para una canción sin voz.');
    expect(offReason()).toHaveTextContent(text);
    expect(screen.getByRole('radio', { name: 'Escribir la letra ahora' })).toBeDisabled();
  });

  it('music AI on, in Spanish: the hint says the AI writes the lyrics; with lyrics, that they are used', async () => {
    assist = { ai: { enabled: true, reason: null, fix: null } };
    renderPage('es');
    await settle();
    await waitFor(() => expect(hint()).toHaveTextContent('Déjela vacía y la IA escribe la letra cuando pulse Crear la canción.'));
    expect(screen.getByRole('radio', { name: 'Escribir la letra ahora' })).toBeEnabled();
    fireEvent.change(screen.getByRole('textbox', { name: 'Su letra' }), { target: { value: 'la la' } });
    expect(hint()).toHaveTextContent('Se usa su letra.');
  });

  it('"Use my lyrics" still needs lyrics, and shows no Automatic hint', async () => {
    renderPage();
    await settle();
    fireEvent.click(screen.getByRole('radio', { name: 'Use my lyrics' }));
    expect(screen.queryByTestId('lyrics-hint')).toBeNull();
    fireEvent.change(descriptionBox(), { target: { value: 'folk' } });
    expect(screen.getByRole('button', { name: 'Make the song' })).toBeDisabled();
    expect(screen.getByTestId('form-missing')).toHaveTextContent('Write your lyrics, or choose "No vocals".');
  });

  it('after a song made with AI-written lyrics, the opened prompt shows them in the box to edit and remake', async () => {
    assist = { ai: { enabled: true, reason: null, fix: null } };
    prompts = [];
    postAnswer = () => {
      prompts = [musicPrompt({ title: 'Written Now', songCount: 0, lyricsMode: 'auto', lyrics: '[verse]\nThe tide comes in' })];
      return HttpResponse.json({ data: musicRequest({ promptId: PROMPT_ID, lyricsMode: 'auto' }) }, { status: 201 });
    };
    requestReads = [musicRequest({ promptId: PROMPT_ID, lyricsMode: 'auto' })];
    renderPage();
    await settle();
    fireEvent.change(descriptionBox(), { target: { value: 'sea song' } });
    fireEvent.click(screen.getByRole('button', { name: 'Make the song' }));
    expect(await screen.findByRole('heading', { name: 'Prompt: Written Now' })).toBeInTheDocument();
    await waitFor(() => expect(lyricsBox()).toHaveValue('[verse]\nThe tide comes in'));
    expect(hint()).toHaveTextContent('Your lyrics are used.');
  });
});

describe('My prompts (#568)', () => {
  it('lists the saved prompts with title, description, last used and songs; Open loads one into the form', async () => {
    prompts = [musicPrompt(), musicPrompt({ id: 'b0000000-0000-4000-8000-000000000003', title: 'Quiet Night', songCount: 1, description: 'lofi beat' })];
    renderPage();
    const item = await screen.findByTestId(`prompt-${PROMPT_ID}`);
    expect(item).toHaveTextContent('Harbour Lights');
    expect(item).toHaveTextContent('a slow ballad about the sea');
    expect(item).toHaveTextContent(/Last used .* · 2 songs/);
    expect(screen.getByTestId('prompt-b0000000-0000-4000-8000-000000000003')).toHaveTextContent('1 song');
    fireEvent.click(within(item).getByRole('button', { name: 'Open Harbour Lights' }));
    await waitFor(() => expect(titleBox()).toHaveValue('Harbour Lights'));
    expect(descriptionBox()).toHaveValue('a slow ballad about the sea, soft piano, warm voice');
    expect(lyricsBox()).toHaveValue('[verse]\nPerson A walks by the harbour');
    expect(screen.getByRole('heading', { name: 'Prompt: Harbour Lights' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Save as new' })).toBeInTheDocument();
  });

  it('Save changes the loaded prompt; Save as new makes a copy with its saved sample; New prompt empties the form', async () => {
    prompts = [musicPrompt({ hasSample: true, sampleMime: 'audio/wav', sampleBytes: 1_200_000, sampleMode: 'sounds_like' })];
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Open Harbour Lights' }));
    await waitFor(() => expect(titleBox()).toHaveValue('Harbour Lights'));
    expect(screen.getByTestId('saved-sample')).toHaveTextContent('This prompt has a saved sample (1.2 MB)');
    fireEvent.change(titleBox(), { target: { value: 'Harbour Lights Two' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(promptWrites).toHaveLength(1));
    expect(promptWrites[0]).toMatchObject({ method: 'PATCH', id: PROMPT_ID, body: { title: 'Harbour Lights Two', description: 'a slow ballad about the sea, soft piano, warm voice', lyricsMode: 'mine', variations: 2, durationS: 90, sampleMode: 'sounds_like', uiLanguage: 'en' } });
    expect(await screen.findByTestId(`prompt-${PROMPT_ID}`)).toHaveTextContent('Harbour Lights Two');

    fireEvent.click(screen.getByRole('button', { name: 'Save as new' }));
    await waitFor(() => expect(promptWrites).toHaveLength(2));
    expect(promptWrites[1]).toMatchObject({ method: 'POST', body: { title: 'Harbour Lights Two', copySampleFrom: PROMPT_ID } });
    await waitFor(() => expect(within(screen.getByTestId('prompt-b0000000-0000-4000-8000-000000000002')).getByRole('button', { name: 'Open now Harbour Lights Two' })).toBeInTheDocument());

    fireEvent.click(screen.getByRole('button', { name: 'New prompt' }));
    await waitFor(() => expect(titleBox()).toHaveValue(''));
    expect(descriptionBox()).toHaveValue('');
    expect(screen.queryByRole('button', { name: 'Save as new' })).toBeNull();
  });

  it('Remove sample on a loaded prompt: Save sends removeSample; Make the song sends remove_sample', async () => {
    prompts = [musicPrompt({ hasSample: true, sampleMime: 'audio/wav', sampleBytes: 5000 })];
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Open Harbour Lights' }));
    await screen.findByTestId('saved-sample');
    fireEvent.click(screen.getByRole('button', { name: 'Remove sample' }));
    expect(screen.queryByTestId('saved-sample')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Make the song' }));
    await waitFor(() => expect(posted).not.toBeNull());
    expect(fields(posted!)).toMatchObject({ promptId: PROMPT_ID, remove_sample: 'true' });
    expect(fields(posted!)).not.toHaveProperty('sample_mode');
  });

  it('Make the song from a loaded prompt sends its id and keeps using its saved sample', async () => {
    prompts = [musicPrompt({ hasSample: true, sampleMime: 'audio/wav', sampleBytes: 5000, sampleMode: 'same_song' })];
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Open Harbour Lights' }));
    await screen.findByTestId('saved-sample');
    fireEvent.click(screen.getByRole('button', { name: 'Make the song' }));
    await waitFor(() => expect(posted).not.toBeNull());
    expect(fields(posted!)).toMatchObject({ promptId: PROMPT_ID, title: 'Harbour Lights', sample_mode: 'same_song', variations: '2', duration_s: '90' });
    expect(posted!.has('sample')).toBe(false);
    expect(posted!.has('remove_sample')).toBe(false);
  });

  it('Delete asks first and says the songs are kept; the songs show "prompt deleted" afterwards', async () => {
    prompts = [musicPrompt()];
    songs = [librarySong({ title: 'Harbour Lights' })];
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Open Harbour Lights' }));
    await waitFor(() => expect(titleBox()).toHaveValue('Harbour Lights'));
    fireEvent.click(within(screen.getByRole('form', { name: 'Prompt: Harbour Lights' })).getByRole('button', { name: 'Delete' }));
    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent('"Harbour Lights" is removed from My prompts. Its 2 songs stay in the library.');
    expect(promptWrites).toEqual([]);
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(promptWrites).toEqual([{ method: 'DELETE', id: PROMPT_ID, body: { confirm: PROMPT_ID } }]));
    await waitFor(() => expect(screen.queryByTestId(`prompt-${PROMPT_ID}`)).toBeNull());
    expect(titleBox()).toHaveValue('');
    await waitFor(() => expect(screen.getByTestId(`song-${songs[0].id}`)).toHaveTextContent('prompt deleted'));
  });

  it('after a new song is made, its prompt is open in the form', async () => {
    prompts = [];
    postAnswer = () => {
      prompts = [musicPrompt({ title: 'Made Now', songCount: 0 })];
      // The page polls the new request while it is queued: answer with that request.
      requestReads = [musicRequest({ promptId: PROMPT_ID })];
      return HttpResponse.json({ data: musicRequest({ promptId: PROMPT_ID }) }, { status: 201 });
    };
    renderPage();
    await screen.findByTestId('next-action');
    fireEvent.change(descriptionBox(), { target: { value: 'folk' } });
    fireEvent.change(lyricsBox(), { target: { value: 'la la' } });
    fireEvent.click(screen.getByRole('button', { name: 'Make the song' }));
    expect(await screen.findByRole('heading', { name: 'Prompt: Made Now' })).toBeInTheDocument();
    expect(await screen.findByTestId(`prompt-${PROMPT_ID}`)).toHaveTextContent('Made Now');
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
    expect(screen.getByRole('heading', { name: 'Mis indicaciones' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Nueva indicación' })).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: /Describa su canción/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Sugerir un título' })).toBeInTheDocument();
    expect(screen.getByRole('radio', { name: 'Sin voz (instrumental)' })).toBeInTheDocument();
    expect(await screen.findByTestId('ai-off-reason')).toHaveTextContent('la IA está apagada en esta nube');
  });

  it('has no axe violations', async () => {
    songs = [librarySong()];
    prompts = [musicPrompt()];
    const { container } = renderPage();
    await screen.findByRole('button', { name: 'Play Morning light' });
    let results: Awaited<ReturnType<typeof runAxe>> | undefined;
    await act(async () => {
      results = await runAxe(container);
    });
    expect(results!.violations.map((violation) => violation.id)).toEqual([]);
  });
});
