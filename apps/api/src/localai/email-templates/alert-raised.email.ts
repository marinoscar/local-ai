import {
  TRANSACTIONAL_EMAIL_HEADERS,
  html,
  plainText,
  renderLayout,
  resolveEmailRenderContext,
  type EmailRenderContext,
  type RenderedEmail,
} from '@marinoscar/platform-api/email';

import type { AlertNotice } from '../alerts/alert-ports';

// =============================================================================
// "Lab alert" template: `localai.alert_raised` (docs/specs/cloud-app.md section 4.9)
// =============================================================================
//
// Sent to the holders of `lab:operate` for a CRITICAL alert only (warning and info are browser-only). It says what is
// wrong, on which node, and the plain-language fix; the page it points at is `/lab/alerts`. The title of a node-reported
// alert is text the node sent, so it is only ever rendered through `html` (escaped) and, in the subject, with every
// control character and line break removed (a subject is a header no escaping protects).
// =============================================================================

export type AlertRaisedEmailData = AlertNotice;

const oneLine = (value: string): string => value.replace(/[\u0000-\u001f\u007f]+/g, ' ').replace(/\s+/g, ' ').trim();

export function alertRaisedEmail(data: AlertRaisedEmailData, ctx?: EmailRenderContext): RenderedEmail {
  const context = resolveEmailRenderContext(ctx);
  const node = oneLine(data.node);
  const title = oneLine(data.title);
  const fix = oneLine(data.fix);
  const ctaUrl = context.appUrl ? `${context.appUrl.replace(/\/+$/, '')}/lab/alerts` : undefined;
  const subject = `${context.appName}: ${node}: ${title}`;

  const bodyHtml = html`
    <p style="margin:0 0 16px 0;">
      <strong>${node}</strong>: ${title}.
    </p>
    <p style="margin:0 0 16px 0;">What to do: ${fix}</p>
    <p style="margin:0;font-size:13px;line-height:20px;color:#4b5563;">
      Alert code ${data.code}. You are receiving this because you can operate the lab in ${context.appName}. Acknowledge the
      alert on the Alerts page to stop the reminders.
    </p>
  `;

  const htmlDocument = renderLayout(
    { title: 'Lab alert', previewText: `${node}: ${title}`, bodyHtml, ctaLabel: ctaUrl ? 'Open alerts' : undefined, ctaUrl },
    context,
  );
  const text = plainText(
    {
      title: 'Lab alert',
      lines: [
        `${node}: ${title}.`,
        '',
        `What to do: ${fix}`,
        '',
        `Alert code ${data.code}. Acknowledge the alert on the Alerts page to stop the reminders.`,
      ],
      ctaLabel: ctaUrl ? 'Open alerts' : undefined,
      ctaUrl,
    },
    context,
  );
  return { subject, html: htmlDocument, text, headers: { ...TRANSACTIONAL_EMAIL_HEADERS } };
}
