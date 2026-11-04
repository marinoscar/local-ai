// The request id of a gateway request (docs/specs/gateway.md section 3.1 point 3): Nest's RequestIdMiddleware does not
// run on raw routes, so the gateway reads the inner nginx's `X-Request-ID` (`$request_id`, 32 hex) or makes one, and
// echoes it on every response.
import { randomBytes } from 'node:crypto';

/** An id a client or proxy may supply: short and printable, so it is safe in a header, a log line and a JSON body. */
const ACCEPTED_REQUEST_ID = /^[A-Za-z0-9._-]{8,128}$/;

export function newRequestId(): string {
  return randomBytes(16).toString('hex');
}

/** The supplied `x-request-id` when it is well formed, else a new id. */
export function resolveRequestId(header: string | string[] | undefined): string {
  const value = Array.isArray(header) ? header[0] : header;
  return typeof value === 'string' && ACCEPTED_REQUEST_ID.test(value) ? value : newRequestId();
}
