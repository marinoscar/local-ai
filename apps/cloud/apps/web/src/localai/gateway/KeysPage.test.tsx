// The API keys page: the table over the wireframe's rows, Create -> the show-once panel with Copy and three snippets,
// the Revoke confirmation, the admin view, and the axe checks. The transports and the permissions are stubbed.
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import axe from 'axe-core';
import { MemoryRouter } from 'react-router-dom';

import { createdFixture, FIXTURE_TOKEN, KEYS_NOW, keyFixture, keysFixture } from './fixture';
import { KeysPage, type KeysPageProps } from './KeysPage';
import type { CreateKeyInput, GatewayKey, KeyFilters } from './types';

let permissions: string[] = [];
vi.mock('@marinoscar/platform-web/identity/headless', () => ({
  usePermissions: () => ({ hasPermission: (permission: string) => permissions.includes(permission) }),
}));

const DEFAULTS = { expiresInDays: 90, maxUserKeyDays: 365 };

function renderPage(overrides: KeysPageProps = {}) {
  const listKeys = (overrides.listKeys ?? vi.fn(async (_filters: KeyFilters): Promise<GatewayKey[]> => keysFixture())) as ReturnType<typeof vi.fn> & ((filters: KeyFilters) => Promise<GatewayKey[]>);
  const createKey = overrides.createKey ?? vi.fn(async (_input: CreateKeyInput) => createdFixture());
  const revokeKey = overrides.revokeKey ?? vi.fn(async (id: string) => keyFixture({ id, status: 'revoked', revokedAt: KEYS_NOW.toISOString() }));
  const view = render(
    <MemoryRouter>
    <KeysPage
      listKeys={listKeys}
      createKey={createKey}
      revokeKey={revokeKey}
      fetchDefaults={async () => DEFAULTS}
      loadSuggestions={async () => ['qwen3-coder-next-q4km', 'qwen3.5-9b-q4km']}
      now={KEYS_NOW}
      origin="https://ai.marin.cr"
      {...overrides}
    />
    </MemoryRouter>,
  );
  return { ...view, listKeys, createKey, revokeKey };
}

const table = () => screen.findByRole('table', { name: 'API keys' });

describe('KeysPage', () => {
  beforeEach(() => {
    permissions = ['gateway:use'];
    localStorage.clear();
    sessionStorage.clear();
  });

  it('renders the wireframe: name, key prefix, models, limits, expiry, last used, and a Revoke per key', async () => {
    renderPage();
    expect(await screen.findByRole('heading', { level: 1, name: 'API keys' })).toBeInTheDocument();
    const rows = within(await table()).getAllByRole('row').slice(1);
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent('OpenCode laptop');
    expect(rows[0]).toHaveTextContent('lai_Ab3dEf…');
    expect(rows[0]).toHaveTextContent('all');
    expect(rows[0]).toHaveTextContent('60 rpm · 4 · 2M/d');
    expect(rows[0]).toHaveTextContent('2027-01-31');
    expect(rows[0]).toHaveTextContent('2 min ago');
    expect(rows[1]).toHaveTextContent('open-webui (service)');
    expect(rows[1]).toHaveTextContent('600 rpm · 16 · ∞');
    expect(rows[1]).toHaveTextContent('never');
    expect(rows[1]).toHaveTextContent('just now');
    expect(screen.getAllByRole('button', { name: /^Revoke / })).toHaveLength(2);
  });

  it('links to the Models page', async () => {
    renderPage();
    await table();
    expect(screen.getByRole('link', { name: 'Models' })).toHaveAttribute('href', '/gateway/models');
  });

  it('never shows a full token or a hash', async () => {
    const { container } = renderPage();
    await table();
    expect(container.textContent).not.toMatch(/lai_[A-Za-z0-9_-]{43}/);
    expect(container.textContent).not.toMatch(/[0-9a-f]{64}/);
  });

  it('has a Create key button, and a friendly empty state', async () => {
    renderPage({ listKeys: vi.fn(async () => []) });
    expect(await screen.findByTestId('keys-empty')).toHaveTextContent('No keys yet');
    expect(screen.getByRole('button', { name: 'Create key' })).toBeInTheDocument();
  });

  it('says it could not load the keys, and offers to try again', async () => {
    const listKeys = vi.fn().mockRejectedValueOnce(new Error('boom')).mockResolvedValue(keysFixture());
    renderPage({ listKeys });
    expect(await screen.findByText('Could not load the keys.')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await table()).toBeInTheDocument();
  });

  describe('Create key', () => {
    async function openDialog() {
      fireEvent.click(await screen.findByRole('button', { name: 'Create key' }));
      return screen.findByRole('dialog', { name: 'Create key' });
    }

    it('asks for a name, models and an expiry with the default from the settings, and disables Create until the name is set', async () => {
      renderPage();
      const dialog = await openDialog();
      expect(within(dialog).getByLabelText(/Name/)).toBeInTheDocument();
      expect(within(dialog).getByLabelText(/Models/)).toBeInTheDocument();
      expect(within(dialog).getByLabelText('Expires in (days)')).toHaveValue(90);
      expect(within(dialog).getByRole('button', { name: 'Create' })).toBeDisabled();
      fireEvent.change(within(dialog).getByLabelText(/Name/), { target: { value: 'OpenCode laptop' } });
      expect(within(dialog).getByRole('button', { name: 'Create' })).toBeEnabled();
    });

    it('refuses an expiry beyond the longest a user may choose', async () => {
      renderPage();
      const dialog = await openDialog();
      fireEvent.change(within(dialog).getByLabelText(/Name/), { target: { value: 'k' } });
      fireEvent.change(within(dialog).getByLabelText('Expires in (days)'), { target: { value: '366' } });
      expect(within(dialog).getByRole('button', { name: 'Create' })).toBeDisabled();
      fireEvent.change(within(dialog).getByLabelText('Expires in (days)'), { target: { value: '365' } });
      expect(within(dialog).getByRole('button', { name: 'Create' })).toBeEnabled();
    });

    it('sends the name, models and expiry; then shows the key ONCE with Copy, the warning and three snippets', async () => {
      const writeText = vi.fn().mockResolvedValue(undefined);
      Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
      const { createKey, listKeys } = renderPage();
      const dialog = await openDialog();
      fireEvent.change(within(dialog).getByLabelText(/Name/), { target: { value: 'New key' } });
      const models = within(dialog).getByLabelText(/Models/);
      fireEvent.change(models, { target: { value: 'qwen3.5-9b-q4km' } });
      fireEvent.keyDown(models, { key: 'Enter' });
      fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));

      const shown = await screen.findByRole('dialog', { name: 'Your new key' });
      expect(createKey).toHaveBeenCalledWith({ name: 'New key', allowedModels: ['qwen3.5-9b-q4km'], expiresInDays: 90 });
      expect(within(shown).getByText("Store it now; it can't be shown again.")).toBeInTheDocument();
      expect(within(shown).getByLabelText('The new key')).toHaveTextContent(FIXTURE_TOKEN);

      fireEvent.click(within(shown).getByRole('button', { name: 'Copy the key' }));
      await waitFor(() => expect(writeText).toHaveBeenCalledWith(FIXTURE_TOKEN));

      expect(within(shown).getAllByRole('tab').map((tab) => tab.textContent)).toEqual(['curl', 'OpenCode', 'Open WebUI']);
      expect(listKeys.mock.calls.length).toBeGreaterThan(1); // the list refreshed behind the dialog
    });

    it('no snippet contains the token: the key is $LAI_KEY, a file reference, or pasted by hand', async () => {
      renderPage();
      const dialog = await openDialog();
      fireEvent.change(within(dialog).getByLabelText(/Name/), { target: { value: 'k' } });
      fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));
      const shown = await screen.findByRole('dialog', { name: 'Your new key' });
      const tokenBox = within(shown).getByLabelText('The new key');
      expect(within(shown).getByLabelText('curl command')).toHaveTextContent('Authorization: Bearer $LAI_KEY');
      for (const tab of ['curl', 'OpenCode', 'Open WebUI']) {
        fireEvent.click(within(shown).getByRole('tab', { name: tab }));
        for (const panel of within(shown).getAllByRole('tabpanel', { hidden: true })) expect(panel.textContent).not.toContain(FIXTURE_TOKEN);
      }
      fireEvent.click(within(shown).getByRole('tab', { name: 'OpenCode' }));
      expect(within(shown).getByLabelText('OpenCode provider block')).toHaveTextContent('{file:~/.config/opencode/localai-gateway-key}');
      fireEvent.click(within(shown).getByRole('tab', { name: 'Open WebUI' }));
      expect(within(shown).getByText('https://ai.marin.cr/v1')).toBeInTheDocument();
      expect(within(shown).getByText('Bearer')).toBeInTheDocument();
      // only the token box holds it
      expect(tokenBox.textContent).toBe(FIXTURE_TOKEN);
      expect(shown.textContent?.split(FIXTURE_TOKEN).length).toBe(2);
    });

    it('does not close on Escape or a backdrop click while the key is on screen; "I have stored it" closes and drops the token', async () => {
      renderPage();
      const dialog = await openDialog();
      fireEvent.change(within(dialog).getByLabelText(/Name/), { target: { value: 'k' } });
      fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));
      const shown = await screen.findByRole('dialog', { name: 'Your new key' });
      fireEvent.keyDown(shown, { key: 'Escape' });
      const backdrop = document.querySelector('.MuiBackdrop-root');
      if (backdrop) fireEvent.click(backdrop);
      expect(screen.getByRole('dialog', { name: 'Your new key' })).toBeInTheDocument();

      fireEvent.click(within(shown).getByRole('button', { name: 'I have stored it' }));
      await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Your new key' })).not.toBeInTheDocument());
      expect(document.body.textContent).not.toContain(FIXTURE_TOKEN);
      // re-opening the dialog starts empty: the token is not kept anywhere
      fireEvent.click(screen.getByRole('button', { name: 'Create key' }));
      await screen.findByRole('dialog', { name: 'Create key' });
      expect(document.body.textContent).not.toContain(FIXTURE_TOKEN);
    });

    it('never writes the token to localStorage or sessionStorage', async () => {
      renderPage();
      const dialog = await openDialog();
      fireEvent.change(within(dialog).getByLabelText(/Name/), { target: { value: 'k' } });
      fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));
      await screen.findByRole('dialog', { name: 'Your new key' });
      expect(JSON.stringify({ ...localStorage })).not.toContain(FIXTURE_TOKEN);
      expect(JSON.stringify({ ...sessionStorage })).not.toContain(FIXTURE_TOKEN);
    });

    it("shows the API's reason and stays on the form when the key is refused", async () => {
      const createKey = vi.fn().mockRejectedValue(new Error('Unknown model: nope'));
      renderPage({ createKey });
      const dialog = await openDialog();
      fireEvent.change(within(dialog).getByLabelText(/Name/), { target: { value: 'k' } });
      fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));
      expect(await within(dialog).findByText('Unknown model: nope')).toBeInTheDocument();
      expect(screen.queryByRole('dialog', { name: 'Your new key' })).not.toBeInTheDocument();
    });

    it('offers a service key only to a gateway administrator, and lets it never expire', async () => {
      renderPage();
      let dialog = await openDialog();
      expect(within(dialog).queryByRole('radio', { name: /Service key/ })).not.toBeInTheDocument();
      fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
      await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Create key' })).not.toBeInTheDocument());

      permissions = ['gateway:use', 'gateway:admin'];
      const { createKey } = renderPage();
      fireEvent.click((await screen.findAllByRole('button', { name: 'Create key' })).at(-1)!);
      dialog = (await screen.findAllByRole('dialog', { name: 'Create key' })).at(-1)!;
      fireEvent.click(within(dialog).getByRole('radio', { name: /Service key/ }));
      fireEvent.change(within(dialog).getByLabelText('Expires in (days)'), { target: { value: '' } });
      fireEvent.change(within(dialog).getByLabelText(/Name/), { target: { value: 'open-webui' } });
      fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));
      await waitFor(() => expect(createKey).toHaveBeenCalledWith({ name: 'open-webui', kind: 'service', expiresInDays: null }));
    });
  });

  describe('Revoke', () => {
    it('asks first, names the key, and does nothing when cancelled', async () => {
      const { revokeKey } = renderPage();
      fireEvent.click(await screen.findByRole('button', { name: 'Revoke OpenCode laptop' }));
      const dialog = await screen.findByRole('dialog', { name: 'Revoke this key?' });
      expect(dialog).toHaveTextContent('OpenCode laptop');
      expect(dialog).toHaveTextContent('lai_Ab3dEf…');
      expect(dialog).toHaveTextContent('stops working on the next request');
      expect(revokeKey).not.toHaveBeenCalled();
      fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
      await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Revoke this key?' })).not.toBeInTheDocument());
      expect(revokeKey).not.toHaveBeenCalled();
    });

    it('revokes after confirmation, says so, and refreshes the list', async () => {
      const { revokeKey, listKeys } = renderPage();
      fireEvent.click(await screen.findByRole('button', { name: 'Revoke OpenCode laptop' }));
      const dialog = await screen.findByRole('dialog', { name: 'Revoke this key?' });
      const before = listKeys.mock.calls.length;
      fireEvent.click(within(dialog).getByRole('button', { name: 'Revoke' }));
      await waitFor(() => expect(revokeKey).toHaveBeenCalledWith('00000000-0000-4000-8000-000000000001'));
      expect(await screen.findByText(/Revoked lai_Ab3dEf… "OpenCode laptop"/)).toBeInTheDocument();
      await waitFor(() => expect(listKeys.mock.calls.length).toBeGreaterThan(before));
    });

    it("keeps the dialog open with the API's reason when the revoke fails", async () => {
      const revokeKey = vi.fn().mockRejectedValue(new Error('No such key'));
      renderPage({ revokeKey });
      fireEvent.click(await screen.findByRole('button', { name: 'Revoke OpenCode laptop' }));
      const dialog = await screen.findByRole('dialog', { name: 'Revoke this key?' });
      fireEvent.click(within(dialog).getByRole('button', { name: 'Revoke' }));
      expect(await within(dialog).findByText('No such key')).toBeInTheDocument();
    });

    it('a revoked key (shown on request) has no Revoke button and says Revoked', async () => {
      renderPage({ listKeys: vi.fn(async () => [keyFixture({ status: 'revoked', revokedAt: '2026-11-03T10:00:00.000Z' })]) });
      await table();
      expect(screen.queryByRole('button', { name: /^Revoke / })).not.toBeInTheDocument();
      expect(screen.getByTestId('key-status')).toHaveTextContent('Revoked');
    });
  });

  describe('gateway administrator', () => {
    beforeEach(() => {
      permissions = ['gateway:use', 'gateway:admin'];
    });

    it("can list every user's keys, which adds the Owner column and asks the API for ?all", async () => {
      const listKeys = vi.fn(async (filters: KeyFilters) => (filters.all ? [keyFixture({ owner: { email: 'person-a@example.test', displayName: 'Person A' } })] : keysFixture()));
      renderPage({ listKeys });
      await table();
      expect(listKeys).toHaveBeenLastCalledWith({ all: false, includeRevoked: false });
      expect(screen.queryByRole('columnheader', { name: 'Owner' })).not.toBeInTheDocument();
      fireEvent.click(screen.getByRole('checkbox', { name: "Show every user's keys" }));
      expect(await screen.findByRole('columnheader', { name: 'Owner' })).toBeInTheDocument();
      expect(await screen.findByText('person-a@example.test')).toBeInTheDocument();
      expect(listKeys).toHaveBeenLastCalledWith({ all: true, includeRevoked: false });
    });

    it('a contributor has no such toggle', async () => {
      permissions = ['gateway:use'];
      renderPage();
      await table();
      expect(screen.queryByRole('checkbox', { name: "Show every user's keys" })).not.toBeInTheDocument();
      expect(screen.getByRole('checkbox', { name: 'Show revoked keys' })).toBeInTheDocument();
    });
  });

  describe('accessibility', () => {
    it('has no axe violations with keys, in the Create dialog, in the show-once panel and in the Revoke dialog', async () => {
      const run = async () => (await axe.run(document.body, { rules: { 'color-contrast': { enabled: false } } })).violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.html).join(' | ')}`);
      renderPage();
      await table();
      expect(await run()).toEqual([]);

      fireEvent.click(screen.getByRole('button', { name: 'Create key' }));
      const dialog = await screen.findByRole('dialog', { name: 'Create key' });
      expect(await run()).toEqual([]);
      fireEvent.change(within(dialog).getByLabelText(/Name/), { target: { value: 'k' } });
      fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));
      await screen.findByRole('dialog', { name: 'Your new key' });
      expect(await run()).toEqual([]);
      fireEvent.click(screen.getByRole('button', { name: 'I have stored it' }));
      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

      fireEvent.click(screen.getByRole('button', { name: 'Revoke OpenCode laptop' }));
      await screen.findByRole('dialog', { name: 'Revoke this key?' });
      expect(await run()).toEqual([]);
    });

    it('has no axe violations when empty', async () => {
      renderPage({ listKeys: vi.fn(async () => []) });
      await screen.findByTestId('keys-empty');
      const results = await axe.run(document.body, { rules: { 'color-contrast': { enabled: false } } });
      expect(results.violations).toEqual([]);
    });
  });
});
