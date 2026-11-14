import { useLang } from '../lang';
import { codeFix, codeMeaning, type TransportErrorInfo } from './errors';

export interface ErrorNoticeProps {
  error: TransportErrorInfo;
  /** `alert` (default) is announced at once; `status` politely (an info such as "cancelled"). */
  role?: 'alert' | 'status';
  /** Extra class for the host's styling. */
  className?: string;
}

/**
 * ErrorNotice shows what went wrong, the one thing to do and the doctor code (ADR-0009). The agent's own message and
 * fix win; this package's fallback text fills what is missing. No inline style (the Portal CSP): style it through the
 * `lai-error` classes.
 */
export function ErrorNotice({ error, role = 'alert', className }: ErrorNoticeProps) {
  const lang = useLang();
  const message = error.message || codeMeaning(error.code, lang);
  const fix = error.fix || codeFix(error.code, lang);
  const codeWord = lang === 'es' ? 'Código' : 'Code';
  return (
    <div className={`lai-error${role === 'status' ? ' lai-error-info' : ''}${className ? ` ${className}` : ''}`} role={role} data-code={error.code}>
      {message ? <p className="lai-error-message">{message}</p> : null}
      {fix ? <p className="lai-error-fix">{fix}</p> : null}
      <p className="lai-error-code">{`${codeWord}: ${error.code}`}</p>
    </div>
  );
}
