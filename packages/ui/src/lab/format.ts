// Small pure formatters of the Lab components. No locale-dependent output except `formatClock`, which takes the time
// zone explicitly so a test (and the desktop) can pin it.

/** MiB as GB for the VRAM line, one decimal without a trailing ".0" (11469 MiB -> "11.2", 62464 MiB -> "61"). */
export function formatGiB(mib: number): string {
  const rounded = Math.round((mib / 1024) * 10) / 10;
  return Number.isInteger(rounded) ? String(rounded) : rounded.toFixed(1);
}

/** Share of VRAM in use, 0 to 100 (0 when the total is unknown). */
export function vramPercent(usedMib: number, totalMib: number): number {
  if (!(totalMib > 0)) return 0;
  return Math.min(100, Math.max(0, Math.round((usedMib / totalMib) * 100)));
}

/** "just now", "42 s ago", "5 min ago", "3 h ago", "6 days ago"; null for no instant or an unreadable one. */
export function formatAgo(iso: string | null, now: Date): string | null {
  if (iso === null) return null;
  const at = new Date(iso).getTime();
  if (Number.isNaN(at)) return null;
  const seconds = Math.max(0, Math.floor((now.getTime() - at) / 1000));
  if (seconds < 10) return 'just now';
  if (seconds < 60) return `${seconds} s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} min ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return `${hours} h ago`;
  const days = Math.floor(hours / 24);
  return `${days} days ago`;
}

/** "02:14" in the given IANA time zone (the viewer's own when omitted); null for no instant. */
export function formatClock(iso: string | null, timeZone?: string): string | null {
  if (iso === null) return null;
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return null;
  return new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', hour12: false, ...(timeZone ? { timeZone } : {}) }).format(date);
}

/** "2026.11.02-1" -> "11.02-1" (the card's short catalog version). Anything else is returned unchanged. */
export function shortCatalog(version: string): string {
  return version.replace(/^\d{4}\./, '');
}

/** "cuda" -> "CUDA", "vulkan" -> "Vulkan", "cpu" -> "CPU". */
export function backendLabel(backend: 'cuda' | 'vulkan' | 'cpu'): string {
  return backend === 'cuda' ? 'CUDA' : backend === 'cpu' ? 'CPU' : 'Vulkan';
}
