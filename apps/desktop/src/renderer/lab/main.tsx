// Shell page "lab" (docs/specs/desktop.md section 4.8): the node cards of the lab, from the cloud API through the main process.
import type { Lang } from '../../shared/types';
import { mountPage } from '../shared/mount';
import { LabPage } from './LabPage';

const bridge = window.localaiDesktop;
const lang: Lang = bridge?.lang() ?? (navigator.language.toLowerCase().startsWith('es') ? 'es' : 'en');
document.documentElement.lang = lang;
document.title = lang === 'es' ? 'Laboratorio' : 'Lab';

mountPage(<LabPage bridge={bridge} lang={lang} />);
