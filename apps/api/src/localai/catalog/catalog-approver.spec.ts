import { approverNameOf, WITH_APPROVER } from './catalog-approver';

describe('approverNameOf', () => {
  it('returns the display name, trimmed', () => {
    expect(approverNameOf({ approvedBy: { displayName: '  Person A ' } })).toBe('Person A');
  });

  it('returns null when the row was not loaded with the approver, the user is gone or has no name', () => {
    expect(approverNameOf({})).toBeNull();
    expect(approverNameOf({ approvedBy: null })).toBeNull();
    expect(approverNameOf({ approvedBy: { displayName: null } })).toBeNull();
    expect(approverNameOf({ approvedBy: { displayName: '   ' } })).toBeNull();
  });

  it('loads nothing but the display name', () => {
    expect(WITH_APPROVER).toEqual({ approvedBy: { select: { displayName: true } } });
  });

  it('caps a very long name', () => {
    expect(approverNameOf({ approvedBy: { displayName: 'x'.repeat(500) } })).toHaveLength(120);
  });
});
