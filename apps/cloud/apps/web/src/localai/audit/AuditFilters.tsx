// The filter bar of the audit page: node, action (a group prefix or one action), and a date range. Native selects and
// date inputs: they work on a phone, need no script and no network asset, and a screen reader knows them.
import ClearIcon from '@mui/icons-material/Clear';
import { Box, Button, TextField } from '@mui/material';
import type { ChangeEvent } from 'react';

import { rangeIsBackwards } from './format';
import type { AuditFilterOptions, AuditFilterState } from './types';
import { EMPTY_FILTERS } from './types';

export interface AuditFiltersProps {
  filters: AuditFilterState;
  options: AuditFilterOptions | null;
  onChange: (next: AuditFilterState) => void;
}

export function AuditFilters({ filters, options, onChange }: AuditFiltersProps) {
  const set = (patch: Partial<AuditFilterState>) => onChange({ ...filters, ...patch, page: 1 });
  const text = (key: keyof AuditFilterState) => (event: ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => set({ [key]: event.target.value } as Partial<AuditFilterState>);
  const backwards = rangeIsBackwards(filters);
  const active = filters.nodeId !== '' || filters.action !== '' || filters.from !== '' || filters.to !== '';
  const windowDays = options?.defaultWindowDays ?? 30;

  // A node or action the URL names that the options do not list (a typed link) stays selectable.
  const knownNode = options?.nodes.some((node) => node.nodeId === filters.nodeId) ?? false;
  const knownAction = filters.action === '' || (options?.groups.some((group) => group.prefix === filters.action) ?? false) || (options?.actions.some((entry) => entry.action === filters.action) ?? false);

  return (
    <Box
      component="form"
      role="search"
      aria-label="Audit filters"
      onSubmit={(event) => event.preventDefault()}
      sx={{ display: 'grid', gap: 2, gridTemplateColumns: { xs: '1fr', sm: 'repeat(2, minmax(0, 1fr))', lg: 'repeat(4, minmax(0, 1fr))' }, alignItems: 'start' }}
    >
      <TextField select label="Node" size="small" value={filters.nodeId} onChange={text('nodeId')} slotProps={{ select: { native: true }, inputLabel: { shrink: true } }} fullWidth>
        <option value="">All nodes</option>
        {filters.nodeId !== '' && !knownNode ? <option value={filters.nodeId}>{filters.nodeId}</option> : null}
        {(options?.nodes ?? []).map((node) => (
          <option key={node.nodeId} value={node.nodeId}>
            {node.nodeId}
            {node.displayName && node.displayName !== node.nodeId ? ` (${node.displayName})` : ''}
            {node.phase === 'retired' ? ' - retired' : ''}
          </option>
        ))}
      </TextField>

      <TextField select label="Action" size="small" value={filters.action} onChange={text('action')} slotProps={{ select: { native: true }, inputLabel: { shrink: true } }} fullWidth>
        <option value="">All actions</option>
        {filters.action !== '' && !knownAction ? <option value={filters.action}>{filters.action}</option> : null}
        {(options?.groups ?? []).map((group) => (
          <optgroup key={group.prefix} label={group.label}>
            <option value={group.prefix}>{`All ${group.label.toLowerCase()} actions`}</option>
            {(options?.actions ?? [])
              .filter((entry) => entry.action.startsWith(group.prefix))
              .map((entry) => (
                <option key={entry.action} value={entry.action}>
                  {entry.label}
                </option>
              ))}
          </optgroup>
        ))}
      </TextField>

      <TextField
        label="From"
        type="date"
        size="small"
        value={filters.from}
        onChange={text('from')}
        error={backwards}
        helperText={filters.from === '' ? `Empty means the last ${windowDays} days` : undefined}
        slotProps={{ inputLabel: { shrink: true } }}
        fullWidth
      />
      <TextField
        label="To"
        type="date"
        size="small"
        value={filters.to}
        onChange={text('to')}
        error={backwards}
        helperText={backwards ? 'To is before From' : undefined}
        slotProps={{ inputLabel: { shrink: true } }}
        fullWidth
      />

      {active ? (
        <Box sx={{ gridColumn: '1 / -1' }}>
          <Button size="small" startIcon={<ClearIcon />} onClick={() => onChange({ ...EMPTY_FILTERS })}>
            Clear filters
          </Button>
        </Box>
      ) : null}
    </Box>
  );
}
