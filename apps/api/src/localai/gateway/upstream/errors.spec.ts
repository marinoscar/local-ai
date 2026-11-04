import {
  aggregateFailures,
  classifyStatus,
  classifyTransportError,
  normaliseUpstreamRejection,
  upstreamRejectionError,
  type AttemptFailure,
  type AttemptFailureReason,
} from './errors';

const coded = (code: string, extra: Record<string, unknown> = {}): Error => Object.assign(new Error('x'), { code }, extra);

describe('classifyTransportError', () => {
  it.each([
    [coded('ECONNREFUSED'), 'connect'],
    [coded('ENOTFOUND'), 'connect'],
    [coded('ECONNRESET'), 'connect'],
    [coded('UND_ERR_CONNECT_TIMEOUT'), 'connect'],
    [coded('UND_ERR_SOCKET'), 'connect'],
    [coded('UND_ERR_HEADERS_TIMEOUT'), 'timeout'],
    [coded('CERT_HAS_EXPIRED'), 'tls'],
    [coded('DEPTH_ZERO_SELF_SIGNED_CERT'), 'tls'],
    [coded('ERR_TLS_CERT_ALTNAME_INVALID'), 'tls'],
    [coded('ERR_SSL_WRONG_VERSION_NUMBER'), 'tls'],
    [Object.assign(new TypeError('fetch failed'), { cause: coded('UNABLE_TO_VERIFY_LEAF_SIGNATURE') }), 'tls'],
    [Object.assign(new Error('x'), { name: 'AbortError' }), 'aborted'],
    [coded('UND_ERR_ABORTED'), 'aborted'],
    [new Error('something else'), 'connect'],
    ['not an error', 'connect'],
  ])('%#', (error, expected) => {
    expect(classifyTransportError(error)).toBe(expected);
  });
});

describe('classifyStatus (spec 4.2 table)', () => {
  const retry = (reason: string) => ({ kind: 'retry', reason });
  it.each<[number, string, unknown]>([
    [200, '', { kind: 'success' }],
    [204, '', { kind: 'success' }],
    [500, '', retry('status_5xx')],
    [502, 'bad gateway', retry('status_5xx')],
    [503, '', retry('status_5xx')],
    [504, '', retry('status_5xx')],
    [301, '', retry('status_5xx')],
    [401, '', retry('status_401')],
    [400, '{"error":{"message":"model not found"}}', retry('model_not_loaded')],
    [404, '{"error":{"message":"The model \'x\' is not loaded"}}', retry('model_not_loaded')],
    [400, '{"error":{"message":"model is unloaded"}}', retry('model_not_loaded')],
    [400, '{"error":{"message":"the request exceeds the available context size"}}', { kind: 'final' }],
    [400, '{"error":{"message":"invalid model parameter: temperature"}}', { kind: 'final' }],
    [404, '{"error":{"message":"File Not Found"}}', { kind: 'final' }],
    [422, 'model not loaded', { kind: 'final' }],
    [429, '', { kind: 'final' }],
    [403, '', { kind: 'final' }],
  ])('%s %s', (status, body, expected) => {
    expect(classifyStatus(status, body)).toEqual(expected);
  });
});

describe('normaliseUpstreamRejection', () => {
  it('keeps the message and the status, turns a numeric code into upstream_rejected', () => {
    expect(normaliseUpstreamRejection(400, '{"error":{"code":400,"message":"too long","type":"invalid_request_error"}}')).toEqual({
      status: 400,
      message: 'too long',
      code: 'upstream_rejected',
      param: null,
    });
  });
  it('keeps a safe string code and param', () => {
    expect(normaliseUpstreamRejection(422, '{"error":{"code":"context_length_exceeded","message":"m","param":"messages"}}')).toMatchObject({ code: 'context_length_exceeded', param: 'messages' });
  });
  it('drops an unsafe code and a non-JSON body', () => {
    expect(normaliseUpstreamRejection(400, '{"error":{"code":"a b<script>","message":"m"}}').code).toBe('upstream_rejected');
    expect(normaliseUpstreamRejection(400, '<html>nope</html>')).toEqual({ status: 400, message: undefined, code: 'upstream_rejected', param: null });
  });
  it('bounds the message', () => {
    expect(normaliseUpstreamRejection(400, JSON.stringify({ error: { message: 'x'.repeat(5000) } })).message).toHaveLength(1000);
  });
  it('builds a LAI-GW-042 error with the upstream status', () => {
    const error = upstreamRejectionError(413, '{"error":{"message":"m"}}');
    expect(error).toMatchObject({ status: 413, localaiCode: 'LAI-GW-042', code: 'upstream_rejected', message: 'm' });
  });
});

describe('aggregateFailures (dominant failure)', () => {
  const f = (...reasons: AttemptFailureReason[]): AttemptFailure[] => reasons.map((reason, index) => ({ nodeId: `n${index}`, attempt: index + 1, reason, upstreamStatus: null }));
  it.each<[AttemptFailureReason[], string, number]>([
    [['connect', 'connect', 'tls'], 'LAI-GW-040', 502],
    [['status_5xx', 'status_5xx', 'status_5xx'], 'LAI-GW-041', 502],
    [['status_401', 'status_401'], 'LAI-GW-043', 502],
    [['timeout'], 'LAI-GW-044', 504],
    [['model_not_loaded', 'model_not_loaded'], 'LAI-GW-021', 503],
    [['connect', 'status_5xx', 'status_5xx'], 'LAI-GW-041', 502],
    [['connect', 'connect', 'timeout'], 'LAI-GW-040', 502],
    // ties: auth > timeout > error > unreachable > not loaded
    [['connect', 'status_5xx'], 'LAI-GW-041', 502],
    [['connect', 'timeout'], 'LAI-GW-044', 504],
    [['status_5xx', 'status_401'], 'LAI-GW-043', 502],
    [['model_not_loaded', 'connect'], 'LAI-GW-040', 502],
  ])('%j', (reasons, code, status) => {
    const error = aggregateFailures(f(...reasons));
    expect(error.localaiCode).toBe(code);
    expect(error.status).toBe(status);
  });
  it('is the generic no-capacity answer without any failure', () => {
    expect(aggregateFailures([]).localaiCode).toBe('LAI-GW-021');
  });
});
