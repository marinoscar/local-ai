// Administration → AI → "Use AI for music" (#570) over a mocked API (MSW, the real HTTP client): the state first, the
// switch (off by default) and the PUT it sends, a read-only view for ai_config:read alone, a refusal, Spanish, and axe.
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { http, HttpResponse } from 'msw';
import { setupServer } from 'msw/node';
import { MemoryRouter } from 'react-router-dom';

import { runAxe } from '../../__tests__/axe';
import { LanguageProvider, type Lang } from '../../i18n/LanguageProvider';
import { MusicAiSettingsPage } from './MusicAiSettingsPage';

let permissions: string[] = [];
vi.mock('@marinoscar/platform-web/identity/headless', () => ({
  usePermissions: () => ({ hasPermission: (permission: string) => permissions.includes(permission) }),
}));

let stored = { aiEnabled: false };
let puts: unknown[] = [];
let putAnswer: (body: { aiEnabled: boolean }) => Response = (body) => {
  stored = body;
  return HttpResponse.json({ data: stored });
};

const server = setupServer(
  http.get('/api/localai/music/ai-settings', () => HttpResponse.json({ data: stored })),
  http.put('/api/localai/music/ai-settings', async ({ request }) => {
    const body = (await request.json()) as { aiEnabled: boolean };
    puts.push(body);
    return putAnswer(body);
  }),
);

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterAll(() => server.close());
beforeEach(() => {
  permissions = ['ai_config:read', 'ai_config:write'];
  stored = { aiEnabled: false };
  puts = [];
  putAnswer = (body) => {
    stored = body;
    return HttpResponse.json({ data: stored });
  };
});
afterEach(() => {
  cleanup();
  server.resetHandlers();
});

function renderPage(lang: Lang = 'en') {
  return render(
    <MemoryRouter>
      <LanguageProvider initial={lang}>
        <MusicAiSettingsPage />
      </LanguageProvider>
    </MemoryRouter>,
  );
}

const toggle = () => screen.getByRole('switch', { name: 'Use AI for music' });

describe('Use AI for music', () => {
  it('is off by default and says what that means; turning it on sends { aiEnabled: true } and shows the new state', async () => {
    renderPage();
    expect(await screen.findByTestId('music-ai-state')).toHaveTextContent('Music AI is off. On the Music page, empty lyrics make a song with no vocals');
    expect(toggle()).not.toBeChecked();
    expect(screen.getByRole('link', { name: 'Settings → AI' })).toHaveAttribute('href', '/admin/settings/ai');
    fireEvent.click(toggle());
    await waitFor(() => expect(puts).toEqual([{ aiEnabled: true }]));
    expect(await screen.findByText(/Music AI is on\./)).toBeInTheDocument();
    expect(toggle()).toBeChecked();
  });

  it('turns off again', async () => {
    stored = { aiEnabled: true };
    renderPage();
    await waitFor(() => expect(toggle()).toBeChecked());
    fireEvent.click(toggle());
    await waitFor(() => expect(puts).toEqual([{ aiEnabled: false }]));
    await waitFor(() => expect(toggle()).not.toBeChecked());
  });

  it('with ai_config:read alone the switch is shown but cannot be changed', async () => {
    permissions = ['ai_config:read'];
    stored = { aiEnabled: true };
    renderPage();
    await waitFor(() => expect(toggle()).toBeChecked());
    expect(toggle()).toBeDisabled();
    expect(screen.getByTestId('music-ai-read-only')).toHaveTextContent('Only an AI administrator can change this.');
  });

  it('a refused change keeps the old state and shows why', async () => {
    putAnswer = () => HttpResponse.json({ statusCode: 400, message: 'The request is not valid.', details: { reason: 'invalid_request' } }, { status: 400 });
    renderPage();
    await screen.findByTestId('music-ai-state');
    fireEvent.click(toggle());
    expect(await screen.findByRole('alert')).toHaveTextContent('The request is not valid.');
    expect(toggle()).not.toBeChecked();
  });

  it('speaks Spanish, off and on', async () => {
    renderPage('es');
    expect(await screen.findByRole('heading', { level: 1, name: 'Usar IA para música' })).toBeInTheDocument();
    expect(screen.getByTestId('music-ai-state')).toHaveTextContent('La IA para música está apagada.');
    fireEvent.click(screen.getByRole('switch', { name: 'Usar IA para música' }));
    expect(await screen.findByText(/La IA para música está encendida\./)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Ajustes (Settings) → AI' })).toBeInTheDocument();
  });

  it('has no axe violations', async () => {
    const { container } = renderPage();
    await screen.findByTestId('music-ai-state');
    let results: Awaited<ReturnType<typeof runAxe>> | undefined;
    await act(async () => {
      results = await runAxe(container);
    });
    expect(results!.violations.map((violation) => violation.id)).toEqual([]);
  });
});
