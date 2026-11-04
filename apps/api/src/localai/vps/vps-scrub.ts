// Scrubbing of everything the stack-agent's journal puts in the database, an audit row or a response (docs/specs/
// cloud-app.md sections 4.10 and 6). The executor already writes fixed sentences; the cloud does not trust a node, so it
// scrubs again: the lab's token and tailnet rules, AWS keys, and every IPv4 address (a journal never needs one).
import { scrubText } from '../commands/command-scrub';

const REDACTED = '[redacted]';
const AWS_KEY_ID = /\b(?:AKIA|ASIA|AIDA|AROA)[A-Z0-9]{12,}\b/g;
const BASE64_40 = /(?<![A-Za-z0-9/+=])[A-Za-z0-9/+]{40}(?![A-Za-z0-9/+=])/g;
const IPV4 = /(?<![0-9A-Za-z.])(?:\d{1,3}\.){3}\d{1,3}(?![0-9A-Za-z]|\.\d)/g;
const IPV6_TAILNET = /\bfd7a:115c:a1e0(?::[0-9a-f]{1,4}){1,5}\b/gi;
const MAGICDNS = /\b[a-z0-9-]+(?:\.[a-z0-9-]+)*\.ts\.net\b/gi;

/** One string, scrubbed, single-line and at most `maxLength` characters. */
export function scrubVpsText(input: string, maxLength = 500): string {
  const out = scrubText(input)
    .replace(AWS_KEY_ID, REDACTED)
    .replace(BASE64_40, REDACTED)
    .replace(IPV4, REDACTED)
    .replace(IPV6_TAILNET, REDACTED)
    .replace(MAGICDNS, REDACTED)
    .replace(/[\r\n\t]+/g, ' ')
    .trim();
  return out.length > maxLength ? `${out.slice(0, maxLength - 1)}…` : out;
}
