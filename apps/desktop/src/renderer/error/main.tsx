// Shell page "error" (docs/specs/desktop.md section 3.1): `localai://app/error/?code=LAI-DSK-00N&lang=en|es&unit=...`.
import type { Lang } from '../../main/i18n';
import { mountPage } from '../shared/mount';
import { ErrorPage } from './ErrorPage';

interface Bridge {
  errorAction(action: string, code: string): Promise<{ ok: boolean; code?: string }>;
}
declare global {
  interface Window {
    localaiDesktop?: Bridge;
  }
}

const params = new URLSearchParams(window.location.search);
const queryLang = params.get('lang');
const lang: Lang = queryLang === 'es' || queryLang === 'en' ? queryLang : navigator.language.toLowerCase().startsWith('es') ? 'es' : 'en';
document.documentElement.lang = lang;

const bridge = window.localaiDesktop;
mountPage(
  <ErrorPage
    code={params.get('code')}
    lang={lang}
    unit={params.get('unit')}
    act={bridge ? (action, code) => bridge.errorAction(action, code) : undefined}
  />,
);
