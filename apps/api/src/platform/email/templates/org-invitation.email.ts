import {
  SafeHtml,
  TRANSACTIONAL_EMAIL_HEADERS,
  html,
  plainText,
  renderLayout,
  resolveEmailRenderContext,
  type EmailRenderContext,
  type RenderedEmail,
} from '@marinoscar/platform-api/email';

// =============================================================================
// "Invitation to an organization" template — `org.invitation` (#726, PP-6.7)
// =============================================================================
//
// Modelled on `allowlist-invitation.email.ts`, and written for the same
// reader: somebody who may have NO account yet. An organization administrator
// (or, for an organization's first administrator, a deployment operator)
// invited this address; the invitation is claimed when the person signs in
// with it, and the account is created then if it does not exist.
//
// What differs from the allowlist invitation is the ORGANIZATION. A
// multi-organization deployment hosts several customers on one sign-in page,
// so the message names the organization and the role the person will hold in
// it, or "you are invited" would not say to what.
//
// WHAT IS DELIBERATELY NOT RENDERED:
//   - the invite's annotation: an administrator's private remark, exactly as
//     with the allowlist entry's. The payload does not carry it;
//   - any token: the invitation is claimed by signing in with the invited
//     address, so there is no secret to put in a link (and nothing to log).
// =============================================================================

/**
 * Everything the organization invitation renders. No user id, no annotation, no
 * token: the recipient is an email address.
 */
export interface OrgInvitationEmailData {
  /** The invited address, and the one they must sign in with. */
  recipientEmail: string;

  /** The organization's display name. */
  orgName: string;

  /** The org role they will hold (`org_admin`, `contributor`, `viewer`). */
  roleName: string;

  /**
   * Who invited them (display name or email), when known. Disclosed on
   * purpose, for the same reason as `AllowlistInvitationEmailData.invitedBy`:
   * an unattributed invitation reads as phishing.
   */
  invitedBy?: string;

  /** Absolute URL of the sign-in page; the layout omits the button without it. */
  signInUrl?: string;
}

/** The org role as a reader should see it. */
function describeRole(roleName: string): string {
  switch (roleName) {
    case 'org_admin':
      return 'an administrator';
    case 'contributor':
      return 'a contributor';
    case 'viewer':
      return 'a viewer';
    default:
      return roleName;
  }
}

/**
 * Render the organization invitation.
 */
export function orgInvitationEmail(data: OrgInvitationEmailData, ctx?: EmailRenderContext): RenderedEmail {
  const context = resolveEmailRenderContext(ctx);
  const appName = context.appName;
  const invitedBy = data.invitedBy?.trim();
  const role = describeRole(data.roleName);

  // The organization's name stays out of the subject: it is typed by an
  // administrator, and a subject is a header no escaping protects (the
  // template contract forbids markup there). The body names it, escaped.
  const subject = `You have been invited to join an organization on ${appName}`;

  const attribution = invitedBy
    ? html`<p style="margin:0 0 16px 0;">
        <strong>${invitedBy}</strong> sent this invitation, so they are the
        person to ask if you were not expecting it.
      </p>`
    : SafeHtml.EMPTY;

  const bodyHtml = html`
    <p style="margin:0 0 16px 0;">
      <strong>${data.recipientEmail}</strong> has been invited to join
      <strong>${data.orgName}</strong> on ${appName} as ${role}.
    </p>
    ${attribution}
    <p style="margin:0 0 16px 0;">
      There is no password to set and nothing else to accept. Sign in with the
      Google account for that same address and you join the organization on the
      spot. Any other address will not see the invitation.
    </p>
    <p style="margin:0;font-size:13px;line-height:20px;color:#4b5563;">
      If you do not recognise ${data.orgName} or ${appName}, you can ignore this
      message: nothing happens until you sign in.
    </p>
  `;

  const htmlDocument = renderLayout({
    title: `Join ${data.orgName}`,
    previewText: `${data.recipientEmail} has been invited to ${data.orgName}.`,
    bodyHtml,
    ctaLabel: data.signInUrl ? 'Sign in' : undefined,
    ctaUrl: data.signInUrl,
  }, context);

  const lines: string[] = [
    `${data.recipientEmail} has been invited to join ${data.orgName} on ${appName} as ${role}.`,
  ];
  if (invitedBy) {
    lines.push(
      '',
      `${invitedBy} sent this invitation, so they are the person to ask if you were not expecting it.`,
    );
  }
  lines.push(
    '',
    'There is no password to set and nothing else to accept. Sign in with the Google account for',
    'that same address and you join the organization on the spot. Any other address will not see',
    'the invitation.',
    '',
    `If you do not recognise ${data.orgName} or ${appName}, you can ignore this message: nothing`,
    'happens until you sign in.',
  );

  const text = plainText({
    title: `Join ${data.orgName}`,
    lines: [lines[0]!, ...lines.slice(1)],
    ctaLabel: data.signInUrl ? 'Sign in' : undefined,
    ctaUrl: data.signInUrl,
  }, context);

  return {
    subject,
    html: htmlDocument,
    text,
    headers: { ...TRANSACTIONAL_EMAIL_HEADERS },
  };
}
