// A copy button whose confirmation is announced ("Copied") and fades back after a moment. The text is taken from the
// prop, never from the DOM, so a masked credential can be copied without ever being rendered.
import ContentCopyIcon from '@mui/icons-material/ContentCopy';
import CheckIcon from '@mui/icons-material/Check';
import { Button } from '@mui/material';
import { useEffect, useRef, useState } from 'react';

import { useT } from '../../i18n/LanguageProvider';
import { copyText } from './clipboard';

export interface CopyButtonProps {
  text: string;
  /** The visible label, and the accessible name once a context is added: "Copy" -> "Copy install commands". */
  label?: string;
  context?: string;
  size?: 'small' | 'medium';
}

export function CopyButton({ text, label, context, size = 'small' }: CopyButtonProps) {
  const t = useT();
  const shown = label ?? t('Copy', 'Copiar');
  const [state, setState] = useState<'idle' | 'copied' | 'failed'>('idle');
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => () => void (timer.current && clearTimeout(timer.current)), []);

  const onClick = async () => {
    const ok = await copyText(text);
    setState(ok ? 'copied' : 'failed');
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => setState('idle'), 2500);
  };

  return (
    <>
      <Button
        size={size}
        variant="outlined"
        onClick={onClick}
        startIcon={state === 'copied' ? <CheckIcon /> : <ContentCopyIcon />}
        aria-label={context ? `${shown} ${context}` : undefined}
      >
        {state === 'copied' ? t('Copied', 'Copiado') : shown}
      </Button>
      {/* Announced to screen readers; empty while idle. */}
      <span role="status" aria-live="polite" style={{ position: 'absolute', width: 1, height: 1, overflow: 'hidden', clip: 'rect(0 0 0 0)' }}>
        {state === 'copied' ? t('Copied to the clipboard', 'Copiado al portapapeles') : state === 'failed' ? t('Could not copy. Select the text and copy it by hand.', 'No se pudo copiar. Seleccione el texto y cópielo a mano.') : ''}
      </span>
    </>
  );
}
