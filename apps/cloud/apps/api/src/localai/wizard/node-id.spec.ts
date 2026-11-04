import { derivePublicHost, isNodeId, nextFreeNodeId, PUBLIC_HOST_RE, tailnetIpPlaceholder } from './node-id';

describe('node ids', () => {
  it.each(['ai-n1', 'ai-n4', 'ai-n10', 'ai-n9999'])('accepts %s', (id) => expect(isNodeId(id)).toBe(true));
  it.each(['ai-n0', 'ai-n01', 'ai-n10000', 'ai-n', 'ai-n4 ', 'AI-N4', 'ai-n4\n', 'ai-n4-staging', 'localai', ''])('rejects %j', (id) => expect(isNodeId(id)).toBe(false));
});

describe('nextFreeNodeId', () => {
  it('is ai-n1 for an empty lab', () => expect(nextFreeNodeId([])).toBe('ai-n1'));
  it('is max+1, numerically, over every id including a retired one', () => {
    expect(nextFreeNodeId(['ai-n1', 'ai-n2', 'ai-n3'])).toBe('ai-n4');
    expect(nextFreeNodeId(['ai-n2', 'ai-n10'])).toBe('ai-n11');
    // A retired ai-n7 still counts: its row is in the list, so the id is never suggested again.
    expect(nextFreeNodeId(['ai-n1', 'ai-n7'])).toBe('ai-n8');
  });
  it('does not fill gaps', () => expect(nextFreeNodeId(['ai-n1', 'ai-n3'])).toBe('ai-n4'));
  it('ignores ids that are not in the lab shape', () => expect(nextFreeNodeId(['ai-n2', 'ai-n5-run1', 'other', 'ai-n99999'])).toBe('ai-n3'));
  it('is null when the numbers are used up', () => expect(nextFreeNodeId(['ai-n9999'])).toBeNull());
});

describe('public host', () => {
  it('derives <nodeId>.marin.cr', () => expect(derivePublicHost('ai-n4')).toBe('ai-n4.marin.cr'));
  it.each(['ai-n4.marin.cr', 'ai-n4-staging.marin.cr', 'node.example.test', 'a.b.c.example.org'])('accepts %s as a probeable host', (host) => expect(PUBLIC_HOST_RE.test(host)).toBe(true));
  it.each(['localhost', 'ai-n4', '127.0.0.1', '10.0.0.1.', '1.2.3.4', 'AI-N4.marin.cr', 'ai-n4.marin.cr/path', 'ai-n4.marin.cr:8080', 'a b.example.test', '-x.example.test', 'x..example.test', '', 'ai-n4.marin.cr\n'])(
    'refuses %j',
    (host) => expect(PUBLIC_HOST_RE.test(host)).toBe(false),
  );
});

it('names the tailnet address placeholder after the node', () => {
  expect(tailnetIpPlaceholder('ai-n4')).toBe('<TAILNET_IP_AI_N4>');
  expect(tailnetIpPlaceholder('ai-n12')).toBe('<TAILNET_IP_AI_N12>');
});
