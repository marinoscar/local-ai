// Plain text with `code spans` (the registry writes commands in backticks) shown as monospace. Never HTML: the text is
// split and rendered through React, so nothing in it can become markup.
import { Box } from '@mui/material';
import { Fragment } from 'react';

export function FixText({ text }: { text: string }) {
  const parts = text.split('`');
  return (
    <>
      {parts.map((part, index) =>
        index % 2 === 1 ? (
          <Box component="code" key={index} sx={{ fontFamily: 'monospace', bgcolor: 'action.hover', px: 0.5, borderRadius: 0.5 }}>
            {part}
          </Box>
        ) : (
          <Fragment key={index}>{part}</Fragment>
        ),
      )}
    </>
  );
}
