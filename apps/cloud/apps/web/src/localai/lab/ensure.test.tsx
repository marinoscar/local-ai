// "Install what's missing" on the node card and in the shared words (ADR-0010, issue #480): the state first, in the viewer's
// language, from codes only; nothing for a node that does not report it.
import { EnsureLine, LangProvider, NodeCard, ensureCanStart, ensureNeed, ensureStepName, ensureSummary, type EnsureCard } from '@localai/ui';
import { render, screen, within } from '@testing-library/react';

import { runAxe } from '../../__tests__/axe';
import { LAB_NOW, cardOf } from './fixture';

const card = (over: Partial<EnsureCard> = {}): EnsureCard => ({ state: 'needs_you', auto: 'on', done: 0, total: 2, nextKind: 'press_button', nextStep: null, ...over });

describe('ensureSummary: Complete, Installing (n of m), Needs you: one thing, Automatic install is off', () => {
  it('in English', () => {
    expect(ensureSummary(card({ state: 'complete', nextKind: 'none' }))).toEqual({ mark: '✓', tone: 'success', text: 'Complete' });
    expect(ensureSummary(card({ state: 'installing', done: 1, total: 4, nextKind: 'wait' }))).toEqual({ mark: '…', tone: 'info', text: 'Installing (2 of 4)' });
    expect(ensureSummary(card({ state: 'off' }))).toMatchObject({ tone: 'warning', text: 'Automatic install is off' });
    expect(ensureSummary(card()).text).toBe("Needs you: press Install what's missing");
    expect(ensureSummary(card({ nextKind: 'confirm', nextStep: 'whisper.build' })).text).toBe('Needs you: approve speech recognition (build from source) at the computer');
    expect(ensureSummary(card({ nextKind: 'sync' })).text).toBe('Needs you: plug in the AI-ARK drive or sync');
    expect(ensureSummary(card({ nextKind: 'fix' })).text).toBe('Needs you: a part failed to install');
  });

  it('in Spanish', () => {
    expect(ensureSummary(card({ state: 'complete', nextKind: 'none' }), 'es').text).toBe('Completa');
    expect(ensureSummary(card({ state: 'installing', done: 1, total: 4, nextKind: 'wait' }), 'es').text).toBe('Instalando (2 de 4)');
    expect(ensureSummary(card({ state: 'off' }), 'es').text).toBe('La instalación automática está apagada');
    expect(ensureSummary(card(), 'es').text).toBe('Necesita su ayuda: pulsar Instalar lo que falta');
    expect(ensureSummary(card({ nextKind: 'confirm', nextStep: 'restart.kiwix' }), 'es').text).toBe('Necesita su ayuda: aprobar reiniciar la biblioteca en la computadora');
    expect(ensureSummary(card({ nextKind: 'sync' }), 'es').text).toBe('Necesita su ayuda: conectar el disco AI-ARK o sincronizar');
  });

  it('never shows "Installing (5 of 4)" or "(1 of 0)", and an unknown step id shows as it is', () => {
    expect(ensureSummary(card({ state: 'installing', done: 9, total: 4 })).text).toBe('Installing (4 of 4)');
    expect(ensureSummary(card({ state: 'installing', done: 0, total: 0 })).text).toBe('Installing (1 of 1)');
    expect(ensureStepName('tool.unknown')).toBe('tool.unknown');
    expect(ensureNeed({ nextKind: 'confirm', nextStep: null })).toBe('approve an install at the computer');
    expect(ensureNeed({ nextKind: 'none', nextStep: null }, 'es')).toBe('falta algo');
  });

  it('names every part the agent installs in both languages', () => {
    for (const id of ['apt.packages', 'tool.kiwix-tools', 'tool.pmtiles', 'tool.piper', 'whisper.restore', 'whisper.build', 'restart.kiwix']) {
      expect(ensureStepName(id, 'en')).not.toBe(id);
      expect(ensureStepName(id, 'es')).not.toBe(id);
      expect(ensureStepName(id, 'es')).not.toBe(ensureStepName(id, 'en'));
    }
  });

  it('the button is offered when the agent will do it on a press, never for an approval only a person at the node can give', () => {
    expect(ensureCanStart(card())).toBe(true);
    expect(ensureCanStart(card({ nextKind: 'fix' }))).toBe(true);
    expect(ensureCanStart(card({ state: 'off', nextKind: 'none' }))).toBe(true);
    expect(ensureCanStart(card({ nextKind: 'confirm' }))).toBe(false);
    expect(ensureCanStart(card({ nextKind: 'sync' }))).toBe(false);
    expect(ensureCanStart(card({ state: 'installing', nextKind: 'wait' }))).toBe(false);
    expect(ensureCanStart(card({ state: 'complete', nextKind: 'none' }))).toBe(false);
  });
});

describe('the node card', () => {
  const renderNode = (ensure: EnsureCard | null | undefined, lang: 'en' | 'es' = 'en') => {
    const node = { ...cardOf('ai-n1'), ensure };
    return render(
      <LangProvider lang={lang}>
        <NodeCard node={node} now={LAB_NOW} timeZone="UTC" />
      </LangProvider>,
    );
  };

  it('shows the state first, under the name and chips and above the hardware', () => {
    renderNode(card({ state: 'installing', done: 1, total: 3, nextKind: 'wait' }));
    const article = screen.getByTestId('node-card-ai-n1');
    const line = within(article).getByTestId('ensure-line');
    expect(line).toHaveTextContent('Installing (2 of 3)');
    expect(line).toHaveAttribute('data-state', 'installing');
    expect(article.querySelector('[data-testid="health-chip"]')!.compareDocumentPosition(line) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(line.compareDocumentPosition(within(article).getByTestId('model-list')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('speaks Spanish with the language provider', () => {
    renderNode(card({ nextKind: 'confirm', nextStep: 'whisper.build' }), 'es');
    expect(screen.getByTestId('ensure-line')).toHaveTextContent('Necesita su ayuda: aprobar el reconocimiento de voz (compilarlo desde el código) en la computadora');
  });

  it('shows nothing for a node that does not report it (an older agent or cloud): null and absent look the same', () => {
    const none = renderNode(null);
    expect(screen.queryByTestId('ensure-line')).toBeNull();
    none.unmount();
    renderNode(undefined);
    expect(screen.queryByTestId('ensure-line')).toBeNull();
  });

  it('has no accessibility violations, with a mark and words (colour is never the only signal)', async () => {
    const { container } = renderNode(card());
    expect(screen.getByTestId('ensure-line')).toHaveTextContent(/!\s*Needs you/);
    expect((await runAxe(container)).violations).toEqual([]);
  });

  it('EnsureLine alone renders nothing without a block', () => {
    const { container } = render(<EnsureLine ensure={null} />);
    expect(container).toBeEmptyDOMElement();
  });
});
