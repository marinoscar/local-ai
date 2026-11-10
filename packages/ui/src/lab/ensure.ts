// The words of "Install what's missing" (ADR-0010, issue #480) in the viewer's language. The node sends codes (a state, a
// count, the kind of the next action and a step id); the sentence is written here, so a person reading Spanish never
// sees the node's English. Pure functions: the card, the Ark tab and the tests share them.
import type { Lang } from '../lang';
import type { EnsureCard } from './types';

const es = (lang: Lang): boolean => lang === 'es';

/** The names of the parts the agent installs (the ids of agent.md section 3.8.33); an unknown id shows as it is. */
export function ensureStepName(id: string, lang: Lang = 'en'): string {
  const spanish = es(lang);
  switch (id) {
    case 'apt.packages':
      return spanish ? 'paquetes del sistema' : 'system packages';
    case 'tool.kiwix-tools':
      return spanish ? 'el programa de la biblioteca (kiwix)' : 'the library program (kiwix)';
    case 'tool.pmtiles':
      return spanish ? 'la herramienta de mapas (pmtiles)' : 'the maps tool (pmtiles)';
    case 'tool.piper':
      return spanish ? 'el programa de voz (piper)' : 'the voice program (piper)';
    case 'whisper.restore':
      return spanish ? 'el reconocimiento de voz (ya compilado)' : 'speech recognition (ready-made)';
    case 'whisper.build':
      return spanish ? 'el reconocimiento de voz (compilarlo desde el código)' : 'speech recognition (build from source)';
    case 'restart.kiwix':
      return spanish ? 'reiniciar la biblioteca' : 'restart the library';
    default:
      return id;
  }
}

/** The one thing a person has to do, for a node that "needs you". Never the agent's sentence. */
export function ensureNeed(ensure: Pick<EnsureCard, 'nextKind' | 'nextStep'>, lang: Lang = 'en'): string {
  const spanish = es(lang);
  switch (ensure.nextKind) {
    case 'confirm':
      return ensure.nextStep
        ? spanish
          ? `aprobar ${ensureStepName(ensure.nextStep, lang)} en la computadora`
          : `approve ${ensureStepName(ensure.nextStep, lang)} at the computer`
        : spanish
          ? 'aprobar una instalación en la computadora'
          : 'approve an install at the computer';
    case 'sync':
      return spanish ? 'conectar el disco AI-ARK o sincronizar' : 'plug in the AI-ARK drive or sync';
    case 'fix':
      return spanish ? 'una parte no se pudo instalar' : 'a part failed to install';
    case 'press_button':
      return spanish ? 'pulsar Instalar lo que falta' : "press Install what's missing";
    default:
      return spanish ? 'falta algo' : 'something is missing';
  }
}

export type EnsureTone = 'success' | 'info' | 'warning';

export interface EnsureSummary {
  /** A mark that says it without colour: ✓ complete, … installing, ! needs you. */
  mark: string;
  tone: EnsureTone;
  /** "Complete", "Installing (2 of 4)", "Needs you: approve ... at the computer", "Automatic install is off". */
  text: string;
}

/** The state-first sentence of a node: Complete, Installing (n of m), Needs you: one thing, or Automatic install is off. */
export function ensureSummary(ensure: EnsureCard, lang: Lang = 'en'): EnsureSummary {
  const spanish = es(lang);
  switch (ensure.state) {
    case 'complete':
      return { mark: '✓', tone: 'success', text: spanish ? 'Completa' : 'Complete' };
    case 'installing': {
      const n = Math.min(ensure.done + 1, Math.max(ensure.total, 1));
      const total = Math.max(ensure.total, 1);
      return { mark: '…', tone: 'info', text: spanish ? `Instalando (${n} de ${total})` : `Installing (${n} of ${total})` };
    }
    case 'off':
      return { mark: '!', tone: 'warning', text: spanish ? 'La instalación automática está apagada' : 'Automatic install is off' };
    default:
      return { mark: '!', tone: 'warning', text: `${spanish ? 'Necesita su ayuda' : 'Needs you'}: ${ensureNeed(ensure, lang)}` };
  }
}

/**
 * Whether the button "Install what's missing" is worth offering: something is left that the agent will do on its own when
 * asked (not complete, no pass running) and the next thing is not only a person's approval at the node.
 */
export function ensureCanStart(ensure: Pick<EnsureCard, 'state' | 'nextKind'>): boolean {
  if (ensure.state === 'complete' || ensure.state === 'installing') return false;
  return ensure.nextKind === 'press_button' || ensure.nextKind === 'fix' || ensure.state === 'off';
}
