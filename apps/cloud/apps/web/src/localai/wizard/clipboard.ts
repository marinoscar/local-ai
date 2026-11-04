// Copy to the clipboard without any network asset. `navigator.clipboard` needs a secure context (https or localhost);
// when it is missing or refuses, a selected, off-screen textarea and `execCommand('copy')` is the fallback. The text is
// removed from the document at once: a credential must not linger in the DOM.

/** Resolves true when the text was copied. Never throws. */
export async function copyText(text: string): Promise<boolean> {
  try {
    if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    // fall through to the legacy path
  }
  if (typeof document === 'undefined') return false;
  const area = document.createElement('textarea');
  area.value = text;
  area.setAttribute('readonly', '');
  area.setAttribute('aria-hidden', 'true');
  area.style.position = 'fixed';
  area.style.top = '-1000px';
  document.body.appendChild(area);
  try {
    area.select();
    return typeof document.execCommand === 'function' && document.execCommand('copy');
  } catch {
    return false;
  } finally {
    area.remove();
  }
}
