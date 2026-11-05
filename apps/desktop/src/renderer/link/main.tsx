// Shell page "link" (docs/specs/desktop.md section 3.1): the device-link dialog, opened from the menu "Connect to the lab".
import type { Lang } from '../../shared/types';
import { mountPage } from '../shared/mount';
import { LinkDialog } from './LinkDialog';

const bridge = window.localaiDesktop;
const lang: Lang = bridge?.lang() ?? (navigator.language.toLowerCase().startsWith('es') ? 'es' : 'en');
document.documentElement.lang = lang;
document.title = lang === 'es' ? 'Conectar con el laboratorio' : 'Connect to the lab';

mountPage(<LinkDialog bridge={bridge} lang={lang} />);
