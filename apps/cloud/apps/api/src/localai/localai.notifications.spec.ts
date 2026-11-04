import { NOTIFICATION_EVENT_KEY_PATTERN, listNotificationEvents, registerNotifications, registerPlatformNotificationChannels } from '@marinoscar/platform-api/notifications';
import { configureEmailRendering, registerEmailTemplates, registerPlatformEmailTemplates } from '@marinoscar/platform-api/email';

import './email-templates';
import { alertRaisedEmail } from './email-templates/alert-raised.email';
import { nodeEnrolledEmail } from './email-templates/node-enrolled.email';
import { LOCALAI_EMAIL_TEMPLATES } from './email-templates';
import { LOCALAI_NOTIFICATIONS, alertClearedBrowser, commandFailedBrowser, alertRaisedBrowser, nodeEnrolledBrowser } from './localai.notifications';
import type { AlertNotice } from './alerts/alert-ports';

const notice: AlertNotice = { alertId: 'a-1', node: 'ai-n2', kind: 'ark.drive_stale', code: 'LAI-CLD-022', severity: 'critical', title: 'Ark drive not synced for 95 days', fix: 'Plug the AI-ARK drive into the node and press "Sync now".' };

describe('the Local AI notification events', () => {
  it('register with the platform registry as one atomic batch (channels, event keys and email template names all valid)', () => {
    registerPlatformNotificationChannels();
    registerPlatformEmailTemplates();
    registerEmailTemplates(LOCALAI_EMAIL_TEMPLATES);
    expect(() => registerNotifications(LOCALAI_NOTIFICATIONS)).not.toThrow();
    expect(listNotificationEvents().map((event) => event.key)).toEqual(expect.arrayContaining(['localai.alert_raised', 'localai.alert_cleared', 'localai.node_enrolled', 'localai.command_failed']));
  });

  it('uses the permanent keys, each valid for the platform registry (one dot, lower snake case)', () => {
    expect(LOCALAI_NOTIFICATIONS.map((entry) => entry.event.key)).toEqual(['localai.alert_raised', 'localai.alert_cleared', 'localai.node_enrolled', 'localai.command_failed']);
    for (const { event } of LOCALAI_NOTIFICATIONS) {
      expect(event.key).toMatch(NOTIFICATION_EVENT_KEY_PATTERN);
      expect(event.defaultEnabled).toBe(true);
    }
  });

  it('declares the channels of the spec table: raised and enrolled browser + email, cleared browser only', () => {
    const channels = Object.fromEntries(LOCALAI_NOTIFICATIONS.map((entry) => [entry.event.key, entry.event.channels]));
    expect(channels).toEqual({
      'localai.alert_raised': ['browser', 'email'],
      'localai.alert_cleared': ['browser'],
      'localai.node_enrolled': ['browser', 'email'],
      'localai.command_failed': ['browser'],
    });
  });

  it('names an email template for every email channel, and registers the templates it names', () => {
    const names = new Set(LOCALAI_EMAIL_TEMPLATES.map((entry) => entry.name));
    for (const entry of LOCALAI_NOTIFICATIONS) {
      if (entry.event.channels.includes('email')) expect(names.has(entry.emailTemplate ?? '')).toBe(true);
    }
  });

  it('browser rows are one line, say what to do, and open the Alerts page', () => {
    expect(alertRaisedBrowser(notice as never)).toEqual({ title: 'ai-n2: Ark drive not synced for 95 days', body: 'Plug the AI-ARK drive into the node and press "Sync now".', link: '/lab/alerts' });
    expect(alertClearedBrowser(notice as never)).toMatchObject({ title: 'ai-n2: Ark drive not synced for 95 days is resolved', link: '/lab/alerts' });
    expect(nodeEnrolledBrowser({ node: 'ai-n4', displayName: 'Node Four' } as never)).toMatchObject({ title: 'ai-n4 joined the lab', link: '/lab/nodes/ai-n4' });
  });

  it('the command failure row names the command, the node and the fix, and opens the node', () => {
    expect(commandFailedBrowser({ commandId: 'c-1', nodeId: 'ai-n2', nodeName: 'Node Two', type: 'model.load', status: 'failed', errorCode: null, fix: 'Download it first.', message: 'x' } as never)).toEqual({
      title: 'model.load on Node Two failed',
      body: 'Download it first.',
      link: '/lab/nodes/ai-n2',
    });
  });

  it('a node-supplied title cannot add a line to a browser row', () => {
    expect(alertRaisedBrowser({ ...notice, title: 'a\r\nb' } as never).title).toBe('ai-n2: a b');
  });
});

describe('the alert email', () => {
  const ctx = configureEmailRendering({ appName: 'Local AI', appUrl: 'https://cloud.example.test' });

  it('says what is wrong, where, the fix and links the Alerts page', () => {
    const email = alertRaisedEmail(notice, ctx);
    expect(email.subject).toBe('Local AI: ai-n2: Ark drive not synced for 95 days');
    expect(email.text).toContain('ai-n2: Ark drive not synced for 95 days.');
    expect(email.text).toContain('Plug the AI-ARK drive');
    expect(email.text).toContain('https://cloud.example.test/lab/alerts');
    expect(email.html).toContain('https://cloud.example.test/lab/alerts');
    expect(email.headers).toMatchObject({ 'Auto-Submitted': 'auto-generated' });
  });

  it('escapes a title that carries markup and keeps the subject on one line', () => {
    const email = alertRaisedEmail({ ...notice, title: '<script>alert(1)</script>\r\nBcc: x@example.test' }, ctx);
    expect(email.html).not.toContain('<script>');
    expect(email.subject).not.toMatch(/[\r\n]/);
  });

  it('works without an app URL (no button)', () => {
    expect(alertRaisedEmail(notice, configureEmailRendering({ appName: 'Local AI' })).html).not.toContain('/lab/alerts');
  });

  it('is registered under its name and renders through the platform registry shape', () => {
    expect(LOCALAI_EMAIL_TEMPLATES.map((entry) => entry.name)).toEqual(['localai-alert-raised', 'localai-node-enrolled']);
    expect(typeof configureEmailRendering).toBe('function');
  });
});

describe('the node enrolled email', () => {
  it('says which node joined', () => {
    const email = nodeEnrolledEmail({ node: 'ai-n4', displayName: 'Node <b>Four</b>' }, configureEmailRendering({ appName: 'Local AI', appUrl: 'https://cloud.example.test' }));
    expect(email.subject).toBe('Local AI: ai-n4 joined the lab');
    expect(email.html).not.toContain('<b>Four</b>');
    expect(email.text).toContain('ai-n4');
    expect(email.text).toContain('https://cloud.example.test/lab/nodes/ai-n4');
  });
});
