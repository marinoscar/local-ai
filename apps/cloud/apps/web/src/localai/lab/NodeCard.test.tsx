// NodeCard over the 3-node fixture: every piece of information of the wireframe, per node.
import { NodeCard } from '@localai/ui';
import { render, screen, within } from '@testing-library/react';
import { runAxe } from '../../__tests__/axe';

import { LAB_NOW, cardOf } from './fixture';

const renderCard = (nodeId: string, patch: (card: ReturnType<typeof cardOf>) => void = () => undefined) => {
  const node = cardOf(nodeId);
  patch(node);
  return render(<NodeCard node={node} now={LAB_NOW} timeZone="UTC" actions={<button type="button">Open</button>} />);
};

describe('NodeCard: ai-n1 (healthy, online, nothing wrong)', () => {
  it('shows name, health, mode, hardware, the default model, VRAM, ark, sync, drill and versions', () => {
    renderCard('ai-n1');
    const card = screen.getByTestId('node-card-ai-n1');
    expect(within(card).getByRole('heading', { level: 2 })).toHaveTextContent('ai-n1 MS-S1 Max');
    expect(within(card).getByTestId('health-chip')).toHaveTextContent('Healthy');
    expect(within(card).getByTestId('mode-chip')).toHaveTextContent('Online');
    expect(card).toHaveTextContent('Vulkan · 128 GB unified');
    const models = within(card).getByTestId('model-list');
    expect(models).toHaveTextContent('qwen3-coder-next-q4km');
    expect(within(models).getByTitle('default model')).toBeInTheDocument();
    expect(within(card).getByTestId('vram-bar')).toHaveTextContent('61 GB / 96 GB');
    expect(card).toHaveTextContent('Ark: internal');
    expect(within(card).getByTestId('ark-chip')).toHaveTextContent('ready');
    expect(card).toHaveTextContent('drive');
    expect(card).toHaveTextContent('— none');
    expect(card).toHaveTextContent('Sync:');
    expect(card).toHaveTextContent('ok 02:14');
    expect(card).toHaveTextContent('Drill:');
    expect(card).toHaveTextContent('passed');
    expect(within(card).getByTestId('versions')).toHaveTextContent('agent 0.4.1');
    expect(within(card).getByTestId('versions')).toHaveTextContent('cat 11.02-1');
    expect(within(card).queryByText('update available')).toBeNull();
    expect(within(card).queryByTestId('alert-chip')).toBeNull();
    expect(within(card).getByRole('button', { name: 'Open' })).toBeInTheDocument();
  });
});

describe('NodeCard: ai-n2 (a failed sync, a 41-day drive, two alerts)', () => {
  it('shows the failed sync, both ark rows, the drive age and the alert count', () => {
    renderCard('ai-n2');
    const card = screen.getByTestId('node-card-ai-n2');
    expect(card).toHaveTextContent('RTX 4080 12 GB');
    expect(card).toHaveTextContent('CUDA');
    expect(within(card).getByTestId('vram-bar')).toHaveTextContent('11.2 GB / 12 GB');
    const chips = within(card).getAllByTestId('ark-chip');
    expect(chips.map((chip) => chip.textContent)).toEqual(['sync failed', '41 days']);
    expect(chips.map((chip) => chip.getAttribute('data-colour'))).toEqual(['red', 'red']);
    expect(card).toHaveTextContent('failed');
    expect(within(card).getByTestId('alert-chip')).toHaveTextContent('2 alerts, 1 critical');
  });
});

describe('NodeCard: ai-n3 (offline mode, expected silence)', () => {
  it('shows the quiet health, last seen, the last default model, the unplugged drive and the behind flags', () => {
    renderCard('ai-n3');
    const card = screen.getByTestId('node-card-ai-n3');
    expect(within(card).getByTestId('health-chip')).toHaveTextContent('Offline mode (expected)');
    expect(within(card).getByTestId('mode-chip')).toHaveTextContent('Offline');
    expect(card).toHaveTextContent('Last seen 3 h ago');
    const models = within(card).getByTestId('model-list');
    expect(models).toHaveTextContent('qwen3.5-9b-q4km');
    expect(models).toHaveTextContent('(last)');
    expect(within(models).getByTitle('default model')).toBeInTheDocument();
    // A quiet node shows no live VRAM and no loaded list.
    expect(within(card).queryByTestId('vram-bar')).toBeNull();
    expect(models).not.toHaveTextContent('Loaded:');
    const chips = within(card).getAllByTestId('ark-chip');
    expect(chips.map((chip) => chip.textContent)).toEqual(['ready', 'Drive not plugged in · last synced 6 days ago']);
    expect(card).toHaveTextContent('skipped (mode)');
    expect(within(card).getByTestId('versions')).toHaveTextContent('agent 0.4.0');
    expect(within(card).getByTestId('versions')).toHaveTextContent('update available');
    expect(within(card).getByTestId('versions')).toHaveTextContent('cat 10.28-2');
    expect(within(card).getByTestId('versions')).toHaveTextContent('catalog behind');
    expect(within(card).queryByTestId('alert-chip')).toBeNull();
  });
});

describe('NodeCard: other states', () => {
  it('a down node says "Never seen" without a snapshot and shows no model line when nothing was ever default', () => {
    renderCard('ai-n1', (card) => {
      card.health = 'down';
      card.lastStateAt = null;
      card.lastDefault = null;
      card.loadedModels = [];
    });
    const card = screen.getByTestId('node-card-ai-n1');
    expect(within(card).getByTestId('health-chip')).toHaveTextContent('Down');
    expect(card).toHaveTextContent('Never seen');
    expect(within(card).queryByTestId('model-list')).toBeNull();
  });

  it('a serving node with nothing loaded says so', () => {
    renderCard('ai-n1', (card) => {
      card.loadedModels = [];
    });
    expect(screen.getByTestId('model-list')).toHaveTextContent('No model loaded');
  });

  it('a pending node hides the mode chip and a node with no versions says "unknown"', () => {
    renderCard('ai-n1', (card) => {
      card.health = 'quiet';
      card.phase = 'pending';
      card.mode = null;
      card.lastStateAt = null;
      card.agentVersion = null;
      card.catalogVersion = null;
      card.sync = { result: null, finishedAt: null, skipReason: null };
      card.drill = { result: null };
    });
    const card = screen.getByTestId('node-card-ai-n1');
    expect(within(card).getByTestId('health-chip')).toHaveTextContent('Not connected yet');
    expect(within(card).queryByTestId('mode-chip')).toBeNull();
    expect(within(card).getByTestId('versions')).toHaveTextContent('agent unknown');
    expect(within(card).getByTestId('versions')).toHaveTextContent('cat unknown');
    expect(card).toHaveTextContent('never run');
    expect(card).toHaveTextContent('not run');
  });

  it('a card without actions or alerts has no footer, and a single alert reads "1 alert"', () => {
    const node = cardOf('ai-n1');
    const { rerender } = render(<NodeCard node={node} now={LAB_NOW} />);
    expect(screen.queryByTestId('alert-chip')).toBeNull();
    rerender(<NodeCard node={{ ...node, alerts: { active: 1, critical: 0 } }} now={LAB_NOW} />);
    expect(screen.getByTestId('alert-chip')).toHaveTextContent(/^1 alert$/);
  });

  it('never prints a tailnet address or key hint (the card has no such field)', () => {
    const { container } = renderCard('ai-n2');
    expect(container.textContent).not.toMatch(/tailnet|100\.\d+\.\d+\.\d+|nod_|lai_/i);
  });
});

describe('NodeCard accessibility (axe)', () => {
  it.each(['ai-n1', 'ai-n2', 'ai-n3'])('%s has no violations', async (nodeId) => {
    const { container } = renderCard(nodeId);
    const result = await runAxe(container);
    expect(result.violations.map((violation) => `${violation.id}: ${violation.help}`)).toEqual([]);
  });
});
