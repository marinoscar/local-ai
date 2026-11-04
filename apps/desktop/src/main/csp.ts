// Content-Security-Policy for every localai://app response (docs/specs/desktop.md section 3.7).
// `'unsafe-inline'` is for styles only: MUI/Emotion injects style tags. Scripts are same-origin files, never inline.
export const MODULE = 'csp' as const;

const DIRECTIVES = [
  "default-src 'none'",
  "script-src 'self'",
  "style-src 'self' 'unsafe-inline'",
  "img-src 'self' data:",
  "font-src 'self'",
  "connect-src 'self'",
  "frame-ancestors 'none'",
  "base-uri 'none'",
  "form-action 'none'",
  "object-src 'none'",
] as const;

export const SHELL_CSP = DIRECTIVES.join('; ');

/** The security headers added to every localai://app response: success, fallback, error and refusal alike. */
export function shellSecurityHeaders(): Record<string, string> {
  return {
    'Content-Security-Policy': SHELL_CSP,
    'X-Content-Type-Options': 'nosniff',
  };
}

export function applyShellSecurityHeaders(headers: Headers): Headers {
  for (const [name, value] of Object.entries(shellSecurityHeaders())) headers.set(name, value);
  return headers;
}
