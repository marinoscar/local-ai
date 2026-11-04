// The certificate step (docs/specs/cloud-app.md section 4.10; runbook 05 step 25): certbot in a container, webroot
// challenge through the shared proxy, then a check that the renewal configuration points at the paths the proxy's
// renewal script uses. The command is the runbook's, as a fixed argument array.
import { X509Certificate } from 'node:crypto';
import { promises as fs } from 'node:fs';
import { join } from 'node:path';

import type { CommandRunner } from './vps-process.js';
import { EMAIL_RE, VPS_HOST_RE, VpsInputError } from './vps-host.js';

/** Reads the end date of a certificate file. Null when the file cannot be read as a certificate. */
export interface CertificateReader {
  notAfter(path: string): Promise<Date | null>;
}

export class NodeCertificateReader implements CertificateReader {
  async notAfter(path: string): Promise<Date | null> {
    try {
      const pem = await fs.readFile(path);
      return new Date(new X509Certificate(pem).validTo);
    } catch {
      return null;
    }
  }
}

export interface CertbotOptions {
  runner: CommandRunner;
  proxyDir: string;
  email: string;
  image?: string;
  reader?: CertificateReader;
  now?: () => Date;
}

export type CertificateOutcome = 'present' | 'issued';

export class CertbotRefusedError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'CertbotRefusedError';
  }
}

/** A certificate with at least this many days left is left alone. */
export const RENEW_WITHIN_DAYS = 21;

const DAY_MS = 24 * 60 * 60 * 1000;
const IMAGE_RE = /^[a-z0-9][a-z0-9._/-]{0,100}(?::[A-Za-z0-9._-]{1,64})?$/;

export class Certbot {
  private readonly runner: CommandRunner;
  private readonly letsencrypt: string;
  private readonly webroot: string;
  private readonly email: string;
  private readonly image: string;
  private readonly reader: CertificateReader;
  private readonly now: () => Date;

  constructor(options: CertbotOptions) {
    if (!EMAIL_RE.test(options.email)) throw new VpsInputError('The certificate contact e-mail is not valid.');
    const image = options.image ?? 'certbot/certbot:latest';
    if (!IMAGE_RE.test(image)) throw new VpsInputError('The certbot image name is not valid.');
    if (!options.proxyDir.startsWith('/') || options.proxyDir.includes('..') || /[\0\n\r:]/.test(options.proxyDir)) throw new VpsInputError('The proxy directory is not valid.');
    this.runner = options.runner;
    this.letsencrypt = join(options.proxyDir, 'letsencrypt');
    this.webroot = join(options.proxyDir, 'webroot');
    this.email = options.email;
    this.image = image;
    this.reader = options.reader ?? new NodeCertificateReader();
    this.now = options.now ?? (() => new Date());
  }

  private paths(host: string) {
    if (!VPS_HOST_RE.test(host)) throw new VpsInputError('The host name is not one this agent may publish.');
    const live = join(this.letsencrypt, 'live', host);
    return { fullchain: join(live, 'fullchain.pem'), privkey: join(live, 'privkey.pem'), renewal: join(this.letsencrypt, 'renewal', `${host}.conf`) };
  }

  /** The certificate's end date, or null when none is in place. */
  async expiresAt(host: string): Promise<Date | null> {
    const { fullchain, privkey } = this.paths(host);
    try {
      await fs.access(privkey);
    } catch {
      return null;
    }
    return this.reader.notAfter(fullchain);
  }

  /** Issues the certificate unless a healthy one is in place. */
  async ensure(host: string): Promise<{ outcome: CertificateOutcome; expiresAt: Date }> {
    const existing = await this.expiresAt(host);
    if (existing !== null && existing.getTime() - this.now().getTime() > RENEW_WITHIN_DAYS * DAY_MS && (await this.renewalConfigOk(host))) {
      return { outcome: 'present', expiresAt: existing };
    }
    const result = await this.runner.run(
      'docker',
      [
        'run',
        '--rm',
        '-v',
        `${this.letsencrypt}:/etc/letsencrypt`,
        '-v',
        `${this.webroot}:/var/www/certbot`,
        this.image,
        'certonly',
        '--webroot',
        '-w',
        '/var/www/certbot',
        '-d',
        host,
        '--email',
        this.email,
        '--agree-tos',
        '--no-eff-email',
        '--non-interactive',
      ],
      { timeoutMs: 300_000 },
    );
    // certbot's output names the account and the challenge: it is not copied anywhere.
    if (result.exitCode !== 0) throw new CertbotRefusedError('certbot did not issue the certificate. Check that the name reaches this server on port 80.');
    const expiresAt = await this.expiresAt(host);
    if (expiresAt === null) throw new CertbotRefusedError('certbot finished but no certificate is in place.');
    if (!(await this.renewalConfigOk(host))) throw new CertbotRefusedError('The certificate was issued but its renewal settings do not use the shared webroot.');
    return { outcome: 'issued', expiresAt };
  }

  /** The renewal file exists and renews by webroot into the folder the proxy serves. */
  async renewalConfigOk(host: string): Promise<boolean> {
    try {
      const text = await fs.readFile(this.paths(host).renewal, 'utf8');
      return /^\s*authenticator\s*=\s*webroot\s*$/m.test(text) && /^\s*webroot_path\s*=\s*\/var\/www\/certbot\b/m.test(text);
    } catch {
      return false;
    }
  }
}
