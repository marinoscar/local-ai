import { etagOf, ifNoneMatchMatches } from './etag';

const SHA = 'a'.repeat(64);

describe('strong ETag matching', () => {
  it('quotes the digest', () => {
    expect(etagOf(SHA)).toBe(`"${SHA}"`);
  });

  it('matches the current ETag, alone or in a list', () => {
    expect(ifNoneMatchMatches(`"${SHA}"`, SHA)).toBe(true);
    expect(ifNoneMatchMatches(`"other", "${SHA}"`, SHA)).toBe(true);
    expect(ifNoneMatchMatches([`"x"`, `"${SHA}"`], SHA)).toBe(true);
    expect(ifNoneMatchMatches('*', SHA)).toBe(true);
  });

  it('does not match a weak validator, another digest, an unquoted digest or no header', () => {
    expect(ifNoneMatchMatches(`W/"${SHA}"`, SHA)).toBe(false);
    expect(ifNoneMatchMatches(`"${'b'.repeat(64)}"`, SHA)).toBe(false);
    expect(ifNoneMatchMatches(SHA, SHA)).toBe(false);
    expect(ifNoneMatchMatches('', SHA)).toBe(false);
    expect(ifNoneMatchMatches(undefined, SHA)).toBe(false);
  });
});
