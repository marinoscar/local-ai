// The gutter must sit beside the text: same font, size, line height and vertical padding, one number per line, and the
// gutter follows the textarea's scroll. (jsdom has no layout, so alignment is proven by the shared metrics.)
import { act, fireEvent, render, screen } from '@testing-library/react';
import { runAxe } from '../../__tests__/axe';
import { createRef } from 'react';

import { gutterText, lineCount, LineNumberTextarea, TEXT_METRICS, type LineNumberTextareaHandle } from './LineNumberTextarea';

describe('lineCount and gutterText', () => {
  it('counts the lines a textarea shows, trailing newline included', () => {
    expect(lineCount('')).toBe(1);
    expect(lineCount('a')).toBe(1);
    expect(lineCount('a\nb')).toBe(2);
    expect(lineCount('a\nb\n')).toBe(3);
    expect(lineCount('\n\n')).toBe(3);
  });

  it('numbers from 1, one per line', () => {
    expect(gutterText(1)).toBe('1');
    expect(gutterText(4)).toBe('1\n2\n3\n4');
  });
});

describe('LineNumberTextarea', () => {
  it('shows one gutter number per line of the text, and follows edits', () => {
    const { rerender } = render(<LineNumberTextarea label="Contents of models.yaml" value={'a\nb\nc'} onChange={() => undefined} />);
    expect(screen.getByTestId('line-gutter').textContent).toBe('1\n2\n3');
    rerender(<LineNumberTextarea label="Contents of models.yaml" value={'a\nb\nc\n'} onChange={() => undefined} />);
    expect(screen.getByTestId('line-gutter').textContent).toBe('1\n2\n3\n4');
    rerender(<LineNumberTextarea label="Contents of models.yaml" value="" onChange={() => undefined} />);
    expect(screen.getByTestId('line-gutter').textContent).toBe('1');
  });

  it('gives the gutter and the textarea the same font, size, line height and vertical padding', () => {
    render(<LineNumberTextarea label="Contents of models.yaml" value={'a\nb'} onChange={() => undefined} />);
    const gutter = getComputedStyle(screen.getByTestId('line-gutter'));
    const area = getComputedStyle(screen.getByRole('textbox', { name: 'Contents of models.yaml' }));
    for (const property of ['fontFamily', 'fontSize', 'lineHeight', 'paddingTop'] as const) {
      expect(gutter[property]).toBe(area[property]);
      expect(area[property]).toBe(TEXT_METRICS[property]);
    }
    // A pixel line height, so line N of the text and number N never drift apart.
    expect(area.lineHeight).toMatch(/^\d+px$/);
    // No wrapping: a long line must not make a second visual line under one number.
    expect(area.whiteSpace).toBe('pre');
    expect(screen.getByRole('textbox')).toHaveAttribute('wrap', 'off');
  });

  it('scrolls the gutter with the textarea', () => {
    render(<LineNumberTextarea label="Contents of models.yaml" value={Array.from({ length: 100 }, (_, i) => `l${i}`).join('\n')} onChange={() => undefined} />);
    const area = screen.getByRole('textbox') as HTMLTextAreaElement;
    const gutter = screen.getByTestId('line-gutter');
    area.scrollTop = 240;
    fireEvent.scroll(area);
    expect(gutter.scrollTop).toBe(240);
    area.scrollTop = 0;
    fireEvent.scroll(area);
    expect(gutter.scrollTop).toBe(0);
  });

  it('reports typing, and is not editable when read only', () => {
    const onChange = vi.fn();
    const { rerender } = render(<LineNumberTextarea label="Contents of models.yaml" value="a" onChange={onChange} />);
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'ab' } });
    expect(onChange).toHaveBeenCalledWith('ab');
    rerender(<LineNumberTextarea label="Contents of CATALOG_VERSION" value="2026.11.03-1" readOnly />);
    expect(screen.getByRole('textbox')).toHaveAttribute('readonly');
  });

  it('jumps to a line: selects it, focuses the textarea and scrolls the gutter along', () => {
    const ref = createRef<LineNumberTextareaHandle>();
    const value = Array.from({ length: 60 }, (_, i) => `line-${i + 1}`).join('\n');
    render(<LineNumberTextarea ref={ref} label="Contents of models.yaml" value={value} onChange={() => undefined} />);
    const area = screen.getByRole('textbox') as HTMLTextAreaElement;
    act(() => ref.current?.jumpToLine(5));
    expect(document.activeElement).toBe(area);
    expect(area.value.slice(area.selectionStart, area.selectionEnd)).toBe('line-5');
    act(() => ref.current?.jumpToLine(1000));
    expect(area.value.slice(area.selectionStart, area.selectionEnd)).toBe('line-60');
    expect(screen.getByTestId('line-gutter').scrollTop).toBe(area.scrollTop);
  });

  it('has no axe violations', async () => {
    const { container } = render(<LineNumberTextarea label="Contents of models.yaml" value={'a\nb'} onChange={() => undefined} />);
    const result = await runAxe(container);
    expect(result.violations).toEqual([]);
  });
});
