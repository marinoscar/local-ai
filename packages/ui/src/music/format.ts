import type { Lang } from '../lang';

/** formatBytes: `512 B`, `3.4 KB`, `1.2 MB` (decimal point in English, comma in Spanish). */
export function formatBytes(n: number, lang: Lang = 'en'): string {
  const units = ['B', 'KB', 'MB', 'GB'];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  const s = i === 0 ? String(Math.round(v)) : v.toFixed(1);
  return `${lang === 'es' ? s.replace('.', ',') : s} ${units[i]}`;
}

/** formatDuration: `0:45`, `3:05`. */
export function formatDuration(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
}

/** formatDate: the song's creation day and time in the viewer's zone, or the raw text when it is not a date. */
export function formatDate(iso: string, lang: Lang, timeZone?: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(lang === 'es' ? 'es' : 'en', { dateStyle: 'medium', timeStyle: 'short', timeZone });
}

/** fileNameOf: a safe download name for a song (`<title>.<ext>`, the id when the title has no usable letter). */
export function fileNameOf(title: string, id: string, mime: string): string {
  const ext = mime === 'audio/mpeg' ? 'mp3' : mime === 'audio/flac' ? 'flac' : mime === 'audio/ogg' ? 'ogg' : 'wav';
  const base = title
    .normalize('NFKD')
    .replace(/[̀-ͯ]/g, '')
    .replace(/[^A-Za-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 40)
    .toLowerCase();
  return `${base || id}.${ext}`;
}
