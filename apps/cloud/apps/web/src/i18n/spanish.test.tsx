// Spanish in the cloud app (issue #436, ADR-0009 rule 7): the language switch and what it changes (the shared chips and
// node card, the Lab page, the Alerts page, the Add-node wizard), where the choice is remembered, and that axe finds
// nothing in the Spanish pages. English stays the default, and the existing English tests run without a provider.
import { ArkChip, HealthChip, ModeChip, NodeCard, VramBar, formatAgo, healthWord } from '@localai/ui';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import axe from 'axe-core';
import { MemoryRouter } from 'react-router-dom';

import { AlertsPage } from '../localai/alerts/AlertsPage';
import { ALERTS_NOW, alertsFixture } from '../localai/alerts/fixture';
import { LAB_NOW, labFixture } from '../localai/lab/fixture';
import { LabOverviewPage } from '../localai/lab/LabOverviewPage';
import { AddNodeWizard } from '../localai/wizard/AddNodeWizard';
import { createResult, fakeClient, makeToken, NODE_ID } from '../localai/wizard/fixture';
import { initialLang, LanguageProvider } from './LanguageProvider';
import { LanguageSwitch } from './LanguageSwitch';

let permissions: string[] = [];
vi.mock('@marinoscar/platform-web/identity/headless', () => ({
  usePermissions: () => ({ hasPermission: (permission: string) => permissions.includes(permission) }),
}));

const noViolations = async (container: HTMLElement) => {
  // jsdom has no layout, so the contrast rule can only say "needs review"; it is left to the real-browser check.
  const result = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } });
  expect(result.violations.map((violation) => `${violation.id}: ${violation.help}`)).toEqual([]);
};

const inSpanish = (ui: React.ReactElement) => render(<LanguageProvider initial="es">{ui}</LanguageProvider>);

beforeEach(() => {
  permissions = ['lab:read', 'lab:admin', 'lab:operate'];
  localStorage.clear();
  document.documentElement.lang = '';
});

describe('the language and the switch', () => {
  it('starts in English unless the browser or a saved choice says Spanish', () => {
    expect(initialLang()).toBe('en');
    localStorage.setItem('localai.lang', 'es');
    expect(initialLang()).toBe('es');
    localStorage.setItem('localai.lang', 'fr'); // anything unknown is ignored
    expect(initialLang()).toBe('en');
    localStorage.clear();
    const spy = vi.spyOn(window.navigator, 'language', 'get');
    spy.mockReturnValue('es-MX');
    expect(initialLang()).toBe('es');
    spy.mockReturnValue('esperanto');
    expect(initialLang()).toBe('en');
    spy.mockRestore();
  });

  it('switches, remembers the choice and tells the document its language', () => {
    render(
      <LanguageProvider>
        <LanguageSwitch />
      </LanguageProvider>,
    );
    const english = screen.getByRole('button', { name: 'English' });
    const spanish = screen.getByRole('button', { name: 'Español' });
    expect(english).toHaveAttribute('aria-pressed', 'true');
    expect(screen.getByRole('group', { name: 'Language' })).toBeInTheDocument();
    fireEvent.click(spanish);
    expect(spanish).toHaveAttribute('aria-pressed', 'true');
    expect(localStorage.getItem('localai.lang')).toBe('es');
    expect(document.documentElement.lang).toBe('es');
    expect(screen.getByRole('group', { name: 'Idioma' })).toBeInTheDocument();
    fireEvent.click(english);
    expect(localStorage.getItem('localai.lang')).toBe('en');
    expect(document.documentElement.lang).toBe('en');
  });

  it('works when the browser refuses storage', () => {
    const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked');
    });
    render(
      <LanguageProvider>
        <LanguageSwitch />
      </LanguageProvider>,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Español' }));
    expect(screen.getByRole('button', { name: 'Español' })).toHaveAttribute('aria-pressed', 'true');
    setItem.mockRestore();
  });

  it('has an accessible switch in both languages', async () => {
    const { container } = inSpanish(<LanguageSwitch />);
    await noViolations(container);
  });
});

describe('the shared chips and node card', () => {
  it('say the state in Spanish, with an icon, and keep English without a provider', () => {
    expect(healthWord('healthy', 'online', 'enrolled')).toBe('Healthy');
    expect(healthWord('healthy', 'online', 'enrolled', 'es')).toBe('Saludable');
    expect(healthWord('quiet', 'survival', 'enrolled', 'es')).toBe('Modo supervivencia (esperado)');
    expect(healthWord('quiet', 'offline', 'enrolled', 'es')).toBe('Modo sin conexión (esperado)');
    expect(healthWord('quiet', null, 'pending', 'es')).toBe('Aún sin conexión');
    const { container } = inSpanish(
      <>
        <HealthChip health="down" />
        <ModeChip mode="survival" />
        <ModeChip mode="online" />
        <ArkChip colour="amber" label="nivel 2 parcial" subject="interna" />
        <VramBar usedMib={6000} totalMib={8192} />
      </>,
    );
    expect(screen.getByTestId('health-chip')).toHaveTextContent('Caído');
    expect(screen.getAllByTestId('mode-chip').map((chip) => chip.textContent)).toEqual(['Supervivencia', 'En línea']);
    expect(screen.getByTestId('ark-chip')).toHaveAttribute('aria-label', 'interna: requiere atención, nivel 2 parcial');
    expect(screen.getByLabelText(/VRAM en uso/)).toBeInTheDocument();
    return noViolations(container);
  });

  it('formats "ago" in Spanish', () => {
    const now = new Date('2026-11-02T15:00:00Z');
    const at = (seconds: number) => new Date(now.getTime() - seconds * 1000).toISOString();
    expect([5, 42, 300, 3 * 3600, 6 * 86400].map((s) => formatAgo(at(s), now, 'es'))).toEqual(['ahora mismo', 'hace 42 s', 'hace 5 min', 'hace 3 h', 'hace 6 días']);
    expect(formatAgo(at(300), now)).toBe('5 min ago');
  });

  it('renders every node card in Spanish', async () => {
    const { container } = inSpanish(
      <MemoryRouter>
        {labFixture().nodes.map((node) => (
          <NodeCard key={node.nodeId} node={node} now={LAB_NOW} timeZone="UTC" />
        ))}
      </MemoryRouter>,
    );
    const text = container.textContent ?? '';
    for (const word of ['Sincronización:', 'Simulacro:', 'Arca: interna', 'Saludable']) expect(text).toContain(word);
    for (const english of ['Sync:', 'Drill:', 'Ark: internal', 'Healthy', 'Last seen', 'Never seen', 'update available']) expect(text).not.toContain(english);
    await noViolations(container);
  });
});

describe('the Lab page in Spanish', () => {
  it('translates the page, the counts and the buttons', async () => {
    const { container } = inSpanish(
      <MemoryRouter>
        <LabOverviewPage fetchNodes={async () => labFixture()} now={LAB_NOW} timeZone="UTC" />
      </MemoryRouter>,
    );
    expect(await screen.findByRole('heading', { level: 1, name: 'Laboratorio' })).toBeInTheDocument();
    expect(await screen.findByTestId('lab-summary')).toHaveTextContent('3 nodos · 2 en línea · 1 sin conexión · 2 alertas');
    expect(screen.getByTestId('gateway-chip')).toHaveTextContent('Pasarela: 2 nodos');
    expect(screen.getByRole('link', { name: 'Agregar nodo' })).toHaveAttribute('href', '/lab/nodes/new');
    expect(screen.getByRole('link', { name: 'Registro de auditoría' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Ver alertas' })).toBeInTheDocument();
    expect(screen.getAllByRole('link', { name: /^Abrir ai-n/ })).toHaveLength(3);
    await noViolations(container);
  });

  it('says it in Spanish when the lab cannot be read', async () => {
    inSpanish(
      <MemoryRouter>
        <LabOverviewPage fetchNodes={async () => Promise.reject(new Error('down'))} now={LAB_NOW} />
      </MemoryRouter>,
    );
    expect(await screen.findByText('No se pudo cargar el laboratorio.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Reintentar' })).toBeInTheDocument();
  });
});

describe('the Alerts page in Spanish', () => {
  it('translates the chrome, the severities and the ack, and leaves the cloud text as it came', async () => {
    const acknowledgeAlert = vi.fn(async () => alertsFixture().alerts[0]!);
    const { container } = inSpanish(
      <MemoryRouter>
        <AlertsPage
          fetchAlerts={async () => alertsFixture()}
          acknowledgeAlert={acknowledgeAlert}
          fetchNodeOptions={async () => [{ nodeId: 'ai-n1', displayName: 'Node One' }]}
          now={ALERTS_NOW}
        />
      </MemoryRouter>,
    );
    expect(await screen.findByRole('heading', { level: 1, name: 'Alertas' })).toBeInTheDocument();
    const list = await screen.findByRole('list', { name: 'Alertas' });
    const first = within(list).getAllByRole('listitem')[0]!;
    expect(within(first).getByTestId('severity-chip')).toHaveTextContent('crítica');
    expect(within(first).getByRole('button', { name: /^Reconocer:/ })).toHaveTextContent('Reconocer');
    expect(first.textContent).toMatch(/Generada/);
    expect(first.textContent).toMatch(/hace/);
    expect(first.textContent).not.toMatch(/Raised| ago/);
    expect(screen.getByRole('link', { name: 'Volver al laboratorio' })).toBeInTheDocument();
    expect(screen.getByLabelText('Mostrar')).toBeInTheDocument();
    await noViolations(container);
  });
});

describe('the Add-node wizard in Spanish', () => {
  it('speaks Spanish on every step, and the shell commands stay as they are', async () => {
    const token = makeToken();
    const client = fakeClient({ createNode: vi.fn().mockImplementation(async () => createResult(token)) });
    const { container } = inSpanish(
      <MemoryRouter initialEntries={['/lab/nodes/new']}>
        <AddNodeWizard client={client} />
      </MemoryRouter>,
    );
    expect(screen.getByRole('heading', { level: 1, name: 'Agregar nodo' })).toBeInTheDocument();
    expect(screen.getByText(/Paso 1 de 3/)).toHaveTextContent('Describa el nodo');
    await waitFor(() => expect(screen.getByLabelText(/^Id del nodo/)).toHaveValue(NODE_ID));
    expect(screen.queryByLabelText(/IP/)).toBeNull();
    await noViolations(container);

    fireEvent.click(screen.getByRole('button', { name: 'Crear nodo' }));
    await screen.findByRole('heading', { name: /Credencial del nodo/ });
    expect(screen.getByText(/Paso 2 de 3/)).toHaveTextContent('Instale en el nodo');
    expect(screen.getByRole('button', { name: 'Mostrar' })).toBeInTheDocument();
    const commands = screen.getByLabelText('Comandos de instalación');
    expect(commands.textContent).toContain('sha256sum -c'); // a command is not translated
    expect(screen.getByText(/debe mostrar install-node.sh: OK/)).toBeInTheDocument();
    expect(screen.getByText(`Esperando que ${NODE_ID} contacte a la nube …`)).toBeInTheDocument();
    expect(screen.getByText('Esperando que el nodo envíe su clave …')).toBeInTheDocument();
    await noViolations(container);

    fireEvent.click(screen.getByRole('checkbox', { name: /Copié la credencial/ }));
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Continuar a publicar' }));
    });
    expect(await screen.findByRole('list', { name: 'Lista para publicar' })).toBeInTheDocument();
    expect(screen.getByText(/Paso 3 de 3/)).toHaveTextContent('Publíquelo (VPS)');
    expect(screen.getAllByRole('button', { name: /Marcar el paso \d+ como hecho|Comprobar/ }).length).toBeGreaterThan(0);
    expect(screen.getByRole('link', { name: 'Volver al laboratorio' })).toBeInTheDocument();
  });

  it('refuses a non-administrator in Spanish', () => {
    permissions = ['lab:read'];
    inSpanish(
      <MemoryRouter>
        <AddNodeWizard client={fakeClient()} />
      </MemoryRouter>,
    );
    expect(screen.getByTestId('not-allowed')).toHaveTextContent('Solo los administradores pueden agregar un nodo.');
  });
});
