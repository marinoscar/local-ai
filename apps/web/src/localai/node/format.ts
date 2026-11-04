// Small pure formatters of the node detail page.
import { formatGiB } from '@localai/ui';

/** Bytes as "5.5 GB" (GiB, the same unit the VRAM bar prints) or "812 MB" below one GB; "0 B" for nothing. */
export function formatBytes(bytes: number): string {
  if (!(bytes > 0)) return '0 B';
  const mib = bytes / 1_048_576;
  if (mib >= 1024) return `${formatGiB(mib)} GB`;
  if (mib >= 1) return `${Math.round(mib)} MB`;
  return `${Math.max(1, Math.round(bytes / 1024))} KB`;
}

/** "42 s", "3 min 5 s", "2 h 1 min" for a number of seconds. */
export function formatElapsed(seconds: number): string {
  const total = Math.max(0, Math.floor(seconds));
  if (total < 60) return `${total} s`;
  const minutes = Math.floor(total / 60);
  if (minutes < 60) return `${minutes} min${total % 60 ? ` ${total % 60} s` : ''}`;
  const hours = Math.floor(minutes / 60);
  return `${hours} h${minutes % 60 ? ` ${minutes % 60} min` : ''}`;
}

/** "3 Nov 2026, 14:05" in the viewer's own time zone (or `timeZone`); "never" for no instant. */
export function formatWhen(iso: string | null, timeZone?: string): string {
  if (!iso) return 'never';
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return 'unknown';
  return new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false, ...(timeZone ? { timeZone } : {}) }).format(date);
}
