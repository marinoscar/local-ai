import {
  TRANSACTIONAL_EMAIL_HEADERS,
  html,
  plainText,
  renderLayout,
  resolveEmailRenderContext,
  type EmailRenderContext,
  type RenderedEmail,
} from '@marinoscar/platform-api/email';

// =============================================================================
// "A node joined the lab" template: `localai.node_enrolled` (docs/specs/cloud-app.md section 4.9)
// =============================================================================
//
// Sent to the holders of `lab:admin` when a node first contacts the cloud with its credential and becomes `enrolled`.
// The binding guard raises it once per node, after the enrolling write committed.
// =============================================================================

export interface NodeEnrolledEmailData {
  /** The lab node id, `ai-n<N>`. */
  node: string;
  /** The node's display name. */
  displayName: string;
}

const oneLine = (value: string): string => value.replace(/[\u0000-\u001f\u007f]+/g, ' ').replace(/\s+/g, ' ').trim();

export function nodeEnrolledEmail(data: NodeEnrolledEmailData, ctx?: EmailRenderContext): RenderedEmail {
  const context = resolveEmailRenderContext(ctx);
  const node = oneLine(data.node);
  const name = oneLine(data.displayName);
  const ctaUrl = context.appUrl ? `${context.appUrl.replace(/\/+$/, '')}/lab/nodes/${encodeURIComponent(node)}` : undefined;
  const subject = `${context.appName}: ${node} joined the lab`;

  const bodyHtml = html`
    <p style="margin:0 0 16px 0;"><strong>${node}</strong> (${name}) joined the lab.</p>
    <p style="margin:0;font-size:13px;line-height:20px;color:#4b5563;">
      It contacted ${context.appName} with its node credential for the first time. You are receiving this because you
      administer the lab.
    </p>
  `;
  const htmlDocument = renderLayout(
    { title: 'A node joined the lab', previewText: `${node} joined the lab.`, bodyHtml, ctaLabel: ctaUrl ? 'Open the node' : undefined, ctaUrl },
    context,
  );
  const text = plainText(
    {
      title: 'A node joined the lab',
      lines: [`${node} (${name}) joined the lab.`, '', `It contacted ${context.appName} with its node credential for the first time.`],
      ctaLabel: ctaUrl ? 'Open the node' : undefined,
      ctaUrl,
    },
    context,
  );
  return { subject, html: htmlDocument, text, headers: { ...TRANSACTIONAL_EMAIL_HEADERS } };
}
