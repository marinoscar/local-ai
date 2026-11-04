// Rules of the Manuals screen (survival-portal.md sections 4.1.7 and 4.2). Pure functions, so they are
// unit-tested without a browser.

export interface ManualItem {
  id: string;
  title: string;
  url: string;
}

/** Rendered runbooks the grid puts first, by the number at the start of their id. */
const FIRST: Record<string, number> = {
  "01": 0, // Survival mode
  "06": 1, // Rebuild offline
  "07": 2, // Troubleshooting
};

function rank(item: ManualItem): number {
  const m = /^(\d{2})(?:\D|$)/.exec(item.id);
  const r = m ? FIRST[m[1]] : undefined;
  return r === undefined ? FIRST_COUNT : r;
}
const FIRST_COUNT = Object.keys(FIRST).length;

/**
 * orderItems keeps the grid order: survival, rebuild offline, troubleshooting, then every other item
 * in the order the agent sent it. The card and "All manuals" buttons follow the items in the screen.
 */
export function orderItems(items: readonly ManualItem[]): ManualItem[] {
  return items
    .map((item, i) => ({ item, i, r: rank(item) }))
    .sort((a, b) => a.r - b.r || a.i - b.i)
    .map((x) => x.item);
}

const NODE_ID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

/** cardPdfUrl is /docs/survival-card-<node-id>.pdf, or null when the node id is missing or odd. */
export function cardPdfUrl(nodeId: string | undefined | null): string | null {
  return nodeId && NODE_ID.test(nodeId)
    ? `/docs/survival-card-${nodeId}.pdf`
    : null;
}

/** cardHtmlUrl is the same card as a web page (the fallback when the PDF is missing). */
export function cardHtmlUrl(nodeId: string | undefined | null): string | null {
  const pdf = cardPdfUrl(nodeId);
  return pdf ? pdf.replace(/\.pdf$/, ".html") : null;
}

/**
 * cardTarget decides what "Print the Survival card" opens: the PDF when the agent has it, else the
 * .html card, else the list of all manuals (no usable node id).
 */
export async function cardTarget(
  nodeId: string | undefined | null,
  fetcher: typeof fetch = fetch,
): Promise<string> {
  const pdf = cardPdfUrl(nodeId);
  const html = cardHtmlUrl(nodeId);
  if (!pdf || !html) return "/docs/";
  try {
    const res = await fetcher(pdf, { method: "HEAD" });
    if (res.ok) return pdf;
  } catch {
    // The agent did not answer: the page link still gives a clear error in the reader.
  }
  return html;
}

/** Degraded manuals: rendered without the real lab values (LAI-SYN-062). */
export function manualsDegraded(
  cap: { state: string; code?: string } | undefined,
): boolean {
  return cap?.state === "degraded" && cap.code === "LAI-SYN-062";
}
