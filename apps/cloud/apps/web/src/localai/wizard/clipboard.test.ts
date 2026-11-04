import { copyText } from './clipboard';

afterEach(() => {
  vi.restoreAllMocks();
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined });
});

describe('copyText', () => {
  it('uses the async clipboard when there is one', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    expect(await copyText('hello')).toBe(true);
    expect(writeText).toHaveBeenCalledWith('hello');
  });

  it('falls back to a selected textarea when the async clipboard is missing or refuses, and leaves nothing in the document', async () => {
    const exec = vi.fn().mockReturnValue(true);
    Object.defineProperty(document, 'execCommand', { configurable: true, value: exec });
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: vi.fn().mockRejectedValue(new Error('denied')) } });
    expect(await copyText('secret-value')).toBe(true);
    expect(exec).toHaveBeenCalledWith('copy');
    expect(document.querySelectorAll('textarea')).toHaveLength(0);
    expect(document.body.innerHTML).not.toContain('secret-value');
  });

  it('answers false, never throws, when nothing can copy', async () => {
    Object.defineProperty(document, 'execCommand', { configurable: true, value: vi.fn().mockReturnValue(false) });
    expect(await copyText('x')).toBe(false);
    Object.defineProperty(document, 'execCommand', { configurable: true, value: vi.fn().mockImplementation(() => { throw new Error('no'); }) });
    expect(await copyText('x')).toBe(false);
  });
});
