// Texts of the connection-info page in both languages (docs/specs/desktop.md 4.6).
import type { Lang } from '../../shared/types';

interface Bilingual {
  en: string;
  es: string;
}

export const pick = (text: Bilingual, lang: Lang): string => text[lang];

export const STRINGS = {
  title: { en: 'Connection info', es: 'Datos de conexión' },
  intro: {
    en: 'Use these to connect a program on this computer (OpenCode, Open WebUI, a script) to the local model.',
    es: 'Úsalos para conectar un programa de esta computadora (OpenCode, Open WebUI, un script) con el modelo local.',
  },
  addresses: { en: 'Addresses', es: 'Direcciones' },
  local: { en: 'This computer (works without internet)', es: 'Esta computadora (funciona sin internet)' },
  tailnet: { en: 'Over the private network', es: 'Por la red privada' },
  publicUrl: { en: 'This node on the internet (needs internet)', es: 'Este nodo en internet (necesita internet)' },
  lab: { en: 'Lab gateway', es: 'Puerta de enlace del laboratorio' },
  labNote: {
    en: 'Needs a lai_ key created in the cloud app. This computer’s key does not work there.',
    es: 'Necesita una clave lai_ creada en la aplicación en la nube. La clave de esta computadora no funciona allí.',
  },
  models: { en: 'Models on this computer', es: 'Modelos de esta computadora' },
  apiKey: { en: 'API key', es: 'Clave de API' },
  reveal: { en: 'Reveal', es: 'Mostrar' },
  hide: { en: 'Hide', es: 'Ocultar' },
  copy: { en: 'Copy', es: 'Copiar' },
  copied: { en: 'API key copied. It will be cleared from the clipboard in {n} seconds.', es: 'Clave copiada. Se borrará del portapapeles en {n} segundos.' },
  rotate: { en: 'Rotate key…', es: 'Cambiar clave…' },
  rotating: { en: 'Rotating the key. This can take a few minutes…', es: 'Cambiando la clave. Puede tardar unos minutos…' },
  rotatedTitle: { en: 'The key was changed. New key: {fp}', es: 'La clave se cambió. Clave nueva: {fp}' },
  rotatedUpdate: { en: 'Update the old key in:', es: 'Cambia la clave anterior en:' },
  rotatedLocalOnly: { en: 'The lab was not told about the new key (this computer is not linked or is offline).', es: 'El laboratorio no recibió la clave nueva (esta computadora no está vinculada o no tiene conexión).' },
  rotatedAnyway: {
    en: 'The key was changed even though a later step failed. The old key no longer works.',
    es: 'La clave se cambió aunque falló un paso posterior. La clave anterior ya no funciona.',
  },
  tabOpencode: { en: 'OpenCode', es: 'OpenCode' },
  tabWebui: { en: 'Open WebUI', es: 'Open WebUI' },
  tabCurl: { en: 'curl', es: 'curl' },
  opencodeStep1: { en: '1. Save the key in a file only you can read. Run this, paste the key (use Copy), press Enter, then Ctrl+D:', es: '1. Guarda la clave en un archivo que solo tú puedas leer. Ejecuta esto, pega la clave (usa Copiar), pulsa Enter y luego Ctrl+D:' },
  opencodeStep2: { en: '2. Add this provider to your OpenCode configuration:', es: '2. Agrega este proveedor a tu configuración de OpenCode:' },
  webuiIntro: { en: 'In Open WebUI open Admin Settings, Connections, and add an OpenAI connection with:', es: 'En Open WebUI abre Ajustes de administrador, Conexiones, y agrega una conexión OpenAI con:' },
  webuiUrl: { en: 'URL', es: 'URL' },
  webuiEnv: { en: 'Or as environment variables:', es: 'O como variables de entorno:' },
  curlModels: { en: 'List the models (no key needed on the command line):', es: 'Lista los modelos (no hace falta la clave en la línea de comandos):' },
  curlChat: { en: 'Ask a question. KEY is the API key; Reveal fills it in:', es: 'Haz una pregunta. KEY es la clave de API; Mostrar la completa:' },
  loading: { en: 'Loading…', es: 'Cargando…' },
  fix: { en: 'Fix', es: 'Solución' },
  loadFailed: { en: 'The connection info could not be read.', es: 'No se pudieron leer los datos de conexión.' },
  loadFailedFix: { en: 'Check that the Local AI agent is running, then close this window and open it again.', es: 'Comprueba que el agente de Local AI esté funcionando, cierra esta ventana y ábrela de nuevo.' },
  actionFailed: { en: 'That did not work.', es: 'Eso no funcionó.' },
  actionFailedFix: { en: 'Try again. If it keeps failing, run sudo aictl doctor.', es: 'Inténtalo de nuevo. Si sigue fallando, ejecuta sudo aictl doctor.' },
} as const satisfies Record<string, Bilingual>;

export const fill = (text: string, values: Record<string, string | number>): string =>
  text.replace(/\{(\w+)\}/g, (whole, name: string) => (name in values ? String(values[name]) : whole));
