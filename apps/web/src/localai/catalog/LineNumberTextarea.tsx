// A plain <textarea> with a line-number gutter (docs/specs/cloud-app.md 3.11: no CodeMirror, no CDN editor, the
// telemetry slice that brings CodeMirror stays off). The gutter and the textarea share one font, size, line height and
// vertical padding (`TEXT_METRICS`), lines never wrap (`white-space: pre`), and the gutter follows the textarea's
// scroll, so number N sits beside line N.
import { Box } from '@mui/material';
import { forwardRef, useCallback, useImperativeHandle, useRef, type UIEvent } from 'react';

/** The metrics both columns use. Pixel line height: a fractional one would drift from the numbers over a long file. */
export const TEXT_METRICS = {
  fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace',
  fontSize: '13px',
  lineHeight: '20px',
  paddingTop: '8px',
  paddingBottom: '8px',
} as const;

const LINE_HEIGHT_PX = 20;
/** Room for a horizontal scrollbar under the textarea, so the gutter can scroll as far as the text does. */
const SCROLLBAR_ALLOWANCE_PX = 24;

/** The number of lines a textarea shows for `value`: a trailing newline opens one more (empty) line. */
export function lineCount(value: string): number {
  return value.split('\n').length;
}

/** The gutter text: `1\n2\n3`. */
export function gutterText(count: number): string {
  return Array.from({ length: count }, (_, index) => index + 1).join('\n');
}

export interface LineNumberTextareaHandle {
  /** Selects line `line` (1-based), scrolls it into view and focuses the textarea. */
  jumpToLine(line: number): void;
}

export interface LineNumberTextareaProps {
  value: string;
  onChange?(value: string): void;
  /** The accessible name of the textarea (the file it holds). */
  label: string;
  readOnly?: boolean;
  id?: string;
}

export const LineNumberTextarea = forwardRef<LineNumberTextareaHandle, LineNumberTextareaProps>(function LineNumberTextarea(
  { value, onChange, label, readOnly = false, id },
  ref,
) {
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const gutterRef = useRef<HTMLDivElement>(null);
  const count = lineCount(value);

  const syncScroll = useCallback(() => {
    if (textareaRef.current && gutterRef.current) gutterRef.current.scrollTop = textareaRef.current.scrollTop;
  }, []);

  useImperativeHandle(
    ref,
    () => ({
      jumpToLine(line: number) {
        const textarea = textareaRef.current;
        if (!textarea) return;
        const lines = textarea.value.split('\n');
        const target = Math.min(Math.max(1, Math.floor(line)), lines.length);
        let start = 0;
        for (let i = 0; i < target - 1; i += 1) start += lines[i]!.length + 1;
        textarea.focus();
        textarea.setSelectionRange(start, start + lines[target - 1]!.length);
        // Put the line about a third of the way down the box.
        textarea.scrollTop = Math.max(0, (target - 1) * LINE_HEIGHT_PX - textarea.clientHeight / 3);
        syncScroll();
      },
    }),
    [syncScroll],
  );

  return (
    <Box
      sx={{
        display: 'flex',
        height: 'min(60vh, 560px)',
        minHeight: 200,
        border: 1,
        borderColor: 'divider',
        borderRadius: 1,
        bgcolor: 'background.paper',
        overflow: 'hidden',
        '&:focus-within': { outline: '2px solid', outlineColor: 'primary.main', outlineOffset: -1 },
      }}
    >
      <Box
        ref={gutterRef}
        aria-hidden="true"
        data-testid="line-gutter"
        sx={{
          ...TEXT_METRICS,
          paddingBottom: `${SCROLLBAR_ALLOWANCE_PX + 8}px`,
          flex: 'none',
          minWidth: `${String(count).length + 2}ch`,
          px: 1,
          textAlign: 'right',
          whiteSpace: 'pre',
          overflow: 'hidden',
          color: 'text.secondary',
          bgcolor: 'action.hover',
          borderRight: 1,
          borderColor: 'divider',
          userSelect: 'none',
        }}
      >
        {gutterText(count)}
      </Box>
      <Box
        component="textarea"
        ref={textareaRef}
        id={id}
        value={value}
        readOnly={readOnly}
        aria-label={label}
        spellCheck={false}
        autoCapitalize="off"
        autoComplete="off"
        autoCorrect="off"
        wrap="off"
        onScroll={(_event: UIEvent<HTMLTextAreaElement>) => syncScroll()}
        onChange={(event: { target: { value: string } }) => onChange?.(event.target.value)}
        sx={{
          ...TEXT_METRICS,
          flex: 1,
          minWidth: 0,
          px: 1,
          m: 0,
          border: 0,
          outline: 0,
          resize: 'none',
          whiteSpace: 'pre',
          overflow: 'auto',
          tabSize: 2,
          color: 'text.primary',
          bgcolor: readOnly ? 'action.hover' : 'transparent',
        }}
      />
    </Box>
  );
});
