// The audit rows as a table: time, actor, action, target (a link when the row has a page), summary. Below 900 px
// each row stacks as a card with the column names as labels, in CSS only (no second copy of the data, no
// media-query hook), so a phone never scrolls sideways. Explicit ARIA roles keep the table semantics when the layout
// changes to blocks.
import { Box, Link, Table, TableBody, TableCell, TableHead, TableRow, Typography } from '@mui/material';
import { Link as RouterLink } from 'react-router-dom';

import { formatWhen, targetLabel } from './format';
import type { AuditRow } from './types';

export interface AuditTableProps {
  rows: readonly AuditRow[];
  /** Plain-language names for the actions, from the filter options. */
  actionLabels?: ReadonlyMap<string, string>;
  /** Names a time zone (tests); the viewer's own otherwise. */
  timeZone?: string;
}

const COLUMNS = ['Time', 'Actor', 'Action', 'Target', 'What happened'] as const;

/** Visually hidden but still read by a screen reader. */
const visuallyHidden = { position: 'absolute', width: 1, height: 1, p: 0, m: '-1px', overflow: 'hidden', clip: 'rect(0 0 0 0)', whiteSpace: 'nowrap', border: 0 } as const;

export function AuditTable({ rows, actionLabels, timeZone }: AuditTableProps) {
  return (
    // The wrapper clips the visually hidden header row on phones (it is positioned absolutely) and scrolls the table
    // on its own, never the page, should a tablet width be too narrow for five columns.
    <Box sx={{ position: 'relative', overflowX: 'auto', width: '100%' }}>
    <Table
      role="table"
      aria-label="Audit trail"
      size="small"
      sx={{
        // Phones and tablets (below 900 px, where five columns do not fit beside the rail): one card per row, the column name before each value.
        '@media (max-width:899.95px)': {
          display: 'block',
          '& thead': visuallyHidden,
          '& tbody': { display: 'block' },
          '& tr': { display: 'block', borderBottom: 1, borderColor: 'divider', py: 1 },
          '& td': { display: 'block', border: 0, py: 0.25, px: 0, overflowWrap: 'anywhere' },
          '& td::before': { content: 'attr(data-label)', display: 'block', fontSize: '0.75rem', color: 'text.secondary' },
        },
      }}
    >
      <TableHead role="rowgroup">
        <TableRow role="row">
          {COLUMNS.map((column) => (
            <TableCell key={column} role="columnheader" scope="col" sx={{ fontWeight: 600, whiteSpace: 'nowrap' }}>
              {column}
            </TableCell>
          ))}
        </TableRow>
      </TableHead>
      <TableBody role="rowgroup">
        {rows.map((row) => {
          const target = targetLabel(row);
          return (
            <TableRow key={row.id} role="row" hover data-testid={`audit-row-${row.id}`}>
              <TableCell role="cell" data-label="Time" sx={{ whiteSpace: { md: 'nowrap' } }}>
                <time dateTime={row.at}>{formatWhen(row.at, timeZone)}</time>
              </TableCell>
              <TableCell role="cell" data-label="Actor" sx={{ whiteSpace: { md: 'nowrap' }, overflowWrap: { xs: 'anywhere', md: 'normal' } }}>
                {row.actor.label}
              </TableCell>
              <TableCell role="cell" data-label="Action">
                <Typography variant="body2" component="span" sx={{ display: 'block' }}>
                  {actionLabels?.get(row.action) ?? row.action}
                </Typography>
                <Typography variant="caption" color="text.secondary" component="span" sx={{ display: 'block', fontFamily: 'monospace', overflowWrap: 'anywhere' }}>
                  {row.action}
                </Typography>
              </TableCell>
              <TableCell role="cell" data-label="Target" sx={{ overflowWrap: 'anywhere' }}>
                {row.link ? (
                  <Link component={RouterLink} to={row.link} aria-label={`Open ${target}`}>
                    {target}
                  </Link>
                ) : (
                  target
                )}
              </TableCell>
              <TableCell role="cell" data-label="What happened" sx={{ overflowWrap: 'anywhere' }}>
                {row.summary}
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
    </Box>
  );
}
