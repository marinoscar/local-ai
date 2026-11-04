// The status chips: colour + word + icon on every state (colour is never the only signal), and the axe checks.
import { ArkChip, HealthChip, ModeChip, VramBar, healthWord, type ArkColour, type LabHealth, type LabMode } from '@localai/ui';
import { render, screen, within } from '@testing-library/react';
import axe from 'axe-core';

async function violations(container: HTMLElement) {
  // jsdom has no layout or rendered colours: the contrast rule needs both, so it is left to the real-browser check.
  const result = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } });
  return result.violations.map((violation) => `${violation.id}: ${violation.help}`);
}

describe('HealthChip', () => {
  it.each<[LabHealth, LabMode | null, 'enrolled' | 'pending', string]>([
    ['healthy', 'online', 'enrolled', 'Healthy'],
    ['degraded', 'online', 'enrolled', 'Degraded'],
    ['down', 'online', 'enrolled', 'Down'],
    ['quiet', 'offline', 'enrolled', 'Offline mode (expected)'],
    ['quiet', 'survival', 'enrolled', 'Survival mode (expected)'],
    ['quiet', null, 'pending', 'Not connected yet'],
  ])('%s / %s / %s says "%s" with an icon', (health, mode, phase, word) => {
    const { container } = render(<HealthChip health={health} mode={mode} phase={phase} />);
    const chip = screen.getByTestId('health-chip');
    expect(chip).toHaveTextContent(word);
    expect(chip.querySelector('svg')).not.toBeNull();
    expect(chip).toHaveAttribute('data-health', health);
    expect(healthWord(health, mode, phase)).toBe(word);
    return violations(container).then((found) => expect(found).toEqual([]));
  });

  it('uses a different icon and colour class per verdict', () => {
    const rendered = (health: LabHealth) => {
      const { container, unmount } = render(<HealthChip health={health} mode="online" phase="enrolled" />);
      const html = container.innerHTML;
      unmount();
      return html;
    };
    const variants = new Set((['healthy', 'degraded', 'down', 'quiet'] as const).map(rendered));
    expect(variants.size).toBe(4);
    expect(rendered('healthy')).toContain('MuiChip-colorSuccess');
    expect(rendered('degraded')).toContain('MuiChip-colorWarning');
    expect(rendered('down')).toContain('MuiChip-colorError');
  });
});

describe('ModeChip', () => {
  it.each<[LabMode | null, string]>([
    ['online', 'Online'],
    ['offline', 'Offline'],
    ['survival', 'Survival'],
    [null, 'Unknown'],
  ])('%s says "%s" with an icon', async (mode, word) => {
    const { container } = render(<ModeChip mode={mode} />);
    const chip = screen.getByTestId('mode-chip');
    expect(chip).toHaveTextContent(word);
    expect(chip.querySelector('svg')).not.toBeNull();
    expect(await violations(container)).toEqual([]);
  });

  it('draws survival apart from offline (secondary vs warning colour)', () => {
    const { container: survival } = render(<ModeChip mode="survival" />);
    const { container: offline } = render(<ModeChip mode="offline" />);
    expect(survival.innerHTML).toContain('MuiChip-colorSecondary');
    expect(offline.innerHTML).toContain('MuiChip-colorWarning');
  });
});

describe('ArkChip', () => {
  it.each<[ArkColour, string, string]>([
    ['green', 'ready', 'MuiChip-colorSuccess'],
    ['amber', 'tier 2 partial', 'MuiChip-colorWarning'],
    ['red', 'tier 1 missing', 'MuiChip-colorError'],
    ['grey', 'no data yet', 'MuiChip-colorDefault'],
  ])('%s shows "%s" with an icon and a status word in the accessible name', async (colour, label, className) => {
    const { container } = render(<ArkChip colour={colour} label={label} subject="internal" />);
    const chip = screen.getByTestId('ark-chip');
    expect(chip).toHaveTextContent(label);
    expect(chip.querySelector('svg')).not.toBeNull();
    expect(chip.className).toContain(className);
    expect(chip).toHaveAttribute('data-colour', colour);
    expect(chip.getAttribute('aria-label')).toMatch(/^internal: (ready|needs attention|not ready|no data), /);
    expect(await violations(container)).toEqual([]);
  });
});

describe('VramBar', () => {
  it('prints the numbers and exposes a labelled progress bar', async () => {
    const { container } = render(<VramBar usedMib={62464} totalMib={98304} />);
    expect(screen.getByText('61 GB / 96 GB')).toBeInTheDocument();
    const bar = within(screen.getByTestId('vram-bar')).getByRole('progressbar');
    expect(bar).toHaveAttribute('aria-valuenow', '64');
    expect(bar).toHaveAccessibleName('VRAM in use: 61 GB / 96 GB');
    expect(await violations(container)).toEqual([]);
  });

  it.each<[number, string]>([
    [8000, 'MuiLinearProgress-colorPrimary'],
    [8900, 'MuiLinearProgress-colorPrimary'],
    [9000, 'MuiLinearProgress-colorWarning'],
    [9749, 'MuiLinearProgress-colorWarning'],
    [9800, 'MuiLinearProgress-colorError'],
  ])('%i of 10000 MiB gets %s', (used, className) => {
    render(<VramBar usedMib={used} totalMib={10000} />);
    expect(within(screen.getByTestId('vram-bar')).getByRole('progressbar').className).toContain(className);
  });
});
