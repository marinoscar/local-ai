import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { LangProvider } from '../lang';
import { FakeWorkloads, err } from '../test/fakes';
import { WorkloadSwitcher } from './WorkloadSwitcher';

describe('WorkloadSwitcher', () => {
  it('says first what the computer is doing, then one button per workload', async () => {
    const tr = new FakeWorkloads();
    render(<WorkloadSwitcher transport={tr} />);
    expect(screen.getByText('Checking what this computer is doing…')).toBeTruthy();
    expect(await screen.findByText('Now: Coding')).toBeTruthy();
    // The active one is shown but cannot be pressed; the others say what they do.
    const coding = screen.getByRole('button', { name: 'Coding' }) as HTMLButtonElement;
    expect(coding.disabled).toBe(true);
    expect(screen.getByText('In use')).toBeTruthy();
    expect((screen.getByRole('button', { name: 'Switch to Music' }) as HTMLButtonElement).disabled).toBe(false);
  });

  it('never hides a workload that cannot work: it says why, with the code', async () => {
    const tr = new FakeWorkloads();
    render(<WorkloadSwitcher transport={tr} />);
    const video = (await screen.findByRole('button', { name: 'Switch to Video' })) as HTMLButtonElement;
    expect(video.disabled).toBe(true);
    expect(screen.getByText(/LAI-LLM-051/).textContent).toContain('Not on this computer yet: video-a');
  });

  it('shows the progress of a switch and the new state when it is done', async () => {
    const tr = new FakeWorkloads();
    const activated: string[] = [];
    render(<WorkloadSwitcher transport={tr} onActivated={(n) => activated.push(n)} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Switch to Music' }));
    expect(tr.calls[0]?.name).toBe('music');
    expect(tr.calls[0]?.opts?.force).toBe(false);
    expect(screen.getByText('Switching to Music… This can take a few minutes.')).toBeTruthy();
    // Every button waits while a switch runs.
    expect((screen.getByRole('button', { name: 'Switch to Chat' }) as HTMLButtonElement).disabled).toBe(true);
    // Indeterminate first, then a measured step.
    expect(document.querySelector('progress')?.hasAttribute('value')).toBe(false);
    act(() => tr.progress(0.4, 'loading'));
    expect(document.querySelector('progress')?.getAttribute('value')).toBe('0.4');
    expect(screen.getByText('Step: loading')).toBeTruthy();
    await act(async () => tr.succeed('music'));
    expect(await screen.findByText('Music is ready.')).toBeTruthy();
    expect(await screen.findByText('Now: Music')).toBeTruthy();
    expect(activated).toEqual(['music']);
  });

  it('shows a failure with its doctor code and fix, and offers to try again', async () => {
    const tr = new FakeWorkloads();
    render(<WorkloadSwitcher transport={tr} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Switch to Music' }));
    await act(async () => tr.pending?.reject(err('LAI-LLM-052', 'Loading music failed; coding is back.', 'Try again in a minute.')));
    const alert = await screen.findByRole('alert');
    expect(alert.getAttribute('data-code')).toBe('LAI-LLM-052');
    expect(alert.textContent).toContain('Loading music failed; coding is back.');
    expect(alert.textContent).toContain('Try again in a minute.');
    expect(alert.textContent).toContain('Code: LAI-LLM-052');
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(tr.calls).toHaveLength(2);
  });

  it('fills in the fix of a code the transport sent without one', async () => {
    const tr = new FakeWorkloads();
    render(<WorkloadSwitcher transport={tr} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Switch to Music' }));
    await act(async () => tr.pending?.reject(new Error('boom')));
    const alert = await screen.findByRole('alert');
    // An error without a code gets the code of the action: the previous set was restored.
    expect(alert.getAttribute('data-code')).toBe('LAI-LLM-052');
    expect(alert.textContent).toContain('Try again in a minute; if it fails again, restart the computer.');
  });

  it('offers "Switch anyway" after a survival refusal only when the host allows it', async () => {
    const tr = new FakeWorkloads();
    const { unmount } = render(<WorkloadSwitcher transport={tr} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Switch to Music' }));
    await act(async () => tr.pending?.reject(err('LAI-LLM-053')));
    expect((await screen.findByRole('alert')).textContent).toContain('Leave survival mode first, then switch.');
    expect(screen.queryByRole('button', { name: 'Switch anyway' })).toBeNull();
    unmount();

    const tr2 = new FakeWorkloads();
    render(<WorkloadSwitcher transport={tr2} allowForce />);
    fireEvent.click(await screen.findByRole('button', { name: 'Switch to Music' }));
    await act(async () => tr2.pending?.reject(err('LAI-LLM-053')));
    fireEvent.click(await screen.findByRole('button', { name: 'Switch anyway' }));
    expect(tr2.calls[1]?.name).toBe('music');
    expect(tr2.calls[1]?.opts?.force).toBe(true);
  });

  it('says so when the list cannot be read, with a code and a retry', async () => {
    const tr = new FakeWorkloads();
    tr.listError = new Error('network');
    render(<WorkloadSwitcher transport={tr} />);
    expect((await screen.findByRole('alert')).getAttribute('data-code')).toBe('LAI-SVC-116');
    tr.listError = null;
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByText('Now: Coding')).toBeTruthy();
  });

  it('can show only some workloads (the music page shows only Music)', async () => {
    const tr = new FakeWorkloads();
    render(<WorkloadSwitcher transport={tr} only={['music']} />);
    expect(await screen.findByRole('button', { name: 'Switch to Music' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Switch to Chat' })).toBeNull();
  });

  it('speaks Spanish', async () => {
    const tr = new FakeWorkloads();
    render(
      <LangProvider lang="es">
        <WorkloadSwitcher transport={tr} />
      </LangProvider>,
    );
    expect(await screen.findByText('Ahora: Programación')).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'Qué está haciendo esta computadora' })).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Cambiar a Música' }));
    await act(async () => tr.pending?.reject(err('LAI-LLM-051')));
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('Código: LAI-LLM-051');
    expect(alert.textContent).toContain('Úsela en una computadora con una tarjeta gráfica más grande.');
    await waitFor(() => expect(screen.getByRole('button', { name: 'Intentar de nuevo' })).toBeTruthy());
  });
});
