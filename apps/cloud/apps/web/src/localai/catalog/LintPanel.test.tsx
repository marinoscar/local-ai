import { fireEvent, render, screen } from '@testing-library/react';
import { runAxe } from '../../__tests__/axe';

import { PROBLEM } from './fixture';
import { CHECKER_FAILED_CODE, LintPanel, problemLabel } from './LintPanel';

describe('LintPanel', () => {
  it('lists each problem as CODE file:path message and reports a click', () => {
    const onSelect = vi.fn();
    const second = { code: 'LAI-CAT-011', file: 'ark.yaml', path: '', message: 'size_bytes is missing' };
    render(<LintPanel version="2026.11.03-1" status="invalid" lintedAt="2026-11-03T08:00:00Z" lintResult={{ valid: false, problems: [PROBLEM, second] }} onSelect={onSelect} />);
    expect(screen.getByText('Lint problems (2)')).toBeInTheDocument();
    expect(problemLabel(PROBLEM)).toBe('LAI-CAT-011 models.yaml:models[0].source.revision must be a 40-hex commit');
    const first = screen.getByRole('button', { name: 'LAI-CAT-011 models.yaml:models[0].source.revision must be a 40-hex commit' });
    expect(first).toHaveTextContent('LAI-CAT-011 models.yaml:models[0].source.revision must be a 40-hex commit');
    fireEvent.click(first);
    expect(onSelect).toHaveBeenCalledWith(PROBLEM);
    // A problem with no path is shown as the file alone.
    expect(screen.getByRole('button', { name: 'LAI-CAT-011 ark.yaml size_bytes is missing' })).toBeInTheDocument();
  });

  it('says it is checking while no verdict exists', () => {
    render(<LintPanel version="2026.11.03-1" status="draft" lintedAt={null} lintResult={null} />);
    expect(screen.getByRole('status')).toHaveTextContent('Checking the catalog…');
  });

  it('says there are no problems for a valid catalog', () => {
    render(<LintPanel version="2026.11.03-1" status="valid" lintedAt="2026-11-03T08:00:00Z" lintResult={{ valid: true, problems: [] }} />);
    expect(screen.getByRole('status')).toHaveTextContent('No problems found.');
  });

  it('explains a checker failure with its doctor code and the fix, never as a list of problems', () => {
    render(
      <LintPanel
        version="2026.11.03-1"
        status="draft"
        lintedAt="2026-11-03T08:00:00Z"
        lintResult={{ error: { code: CHECKER_FAILED_CODE, reason: 'aictl_missing', message: 'aictl is not installed' } }}
      />,
    );
    expect(screen.getByRole('alert')).toHaveTextContent('The catalog checker failed (LAI-CLD-033). Admin: redeploy the cloud app.');
    expect(screen.queryByRole('list')).toBeNull();
  });

  it('warns that the verdict is about the saved text when the text has changed since', () => {
    render(<LintPanel version="2026.11.03-1" status="invalid" lintedAt="2026-11-03T08:00:00Z" lintResult={{ valid: false, problems: [PROBLEM] }} stale />);
    expect(screen.getByText(/You changed the text after 2026.11.03-1 was saved/)).toBeInTheDocument();
  });

  it('has no axe violations in any state', async () => {
    const states = [
      <LintPanel key="a" version="v" status="invalid" lintedAt="x" lintResult={{ valid: false, problems: [PROBLEM] }} />,
      <LintPanel key="b" version="v" status="draft" lintedAt={null} lintResult={null} />,
      <LintPanel key="c" version="v" status="valid" lintedAt="x" lintResult={{ valid: true, problems: [] }} />,
      <LintPanel key="d" version="v" status="draft" lintedAt="x" lintResult={{ error: { code: CHECKER_FAILED_CODE, reason: 'timeout', message: 'slow' } }} />,
    ];
    for (const state of states) {
      const { container, unmount } = render(state);
      expect((await runAxe(container)).violations).toEqual([]);
      unmount();
    }
  });
});
