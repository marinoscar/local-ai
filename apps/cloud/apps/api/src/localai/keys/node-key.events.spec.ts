import { keyReportReason } from './node-key.events';

describe('keyReportReason', () => {
  it.each([
    [null, 'ab12…9f3c', 'enroll'],
    ['ab12…9f3c', 'ab12…9f3c', 'refresh'],
    ['ab12…9f3c', 'cd34…0e1f', 'rotate'],
  ] as const)('previous %s and new %s is %s', (previous, next, reason) => {
    expect(keyReportReason(previous, next)).toBe(reason);
  });
});
