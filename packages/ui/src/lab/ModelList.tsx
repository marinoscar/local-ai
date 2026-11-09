import StarIcon from '@mui/icons-material/Star';
import { Box, Typography } from '@mui/material';

import { useLang } from '../lang';
import type { LoadedModel } from './types';

export interface ModelListProps {
  /** Models to list; the default one carries a star. */
  models: readonly LoadedModel[];
  /**
   * When set, the node is not serving (offline mode, down): show only this model as the last default, with "(last)".
   * `models` is then ignored.
   */
  lastDefault?: string | null;
  /** The title above the list. Default "Loaded:" ("Cargados:"). */
  title?: string;
}

/** The loaded models, one per line, `★` after the default one (the star has a text alternative). */
export function ModelList({ models, lastDefault, title }: ModelListProps) {
  const es = useLang() === 'es';
  const heading = title ?? (es ? 'Cargados:' : 'Loaded:');
  const showLast = lastDefault !== undefined;
  const rows: Array<{ id: string; isDefault: boolean; last: boolean }> = showLast
    ? lastDefault === null
      ? []
      : [{ id: lastDefault, isDefault: true, last: true }]
    : models.map((model) => ({ id: model.id, isDefault: model.isDefault, last: false }));
  return (
    <Box data-testid="model-list">
      <Typography variant="caption" color="text.secondary" component="div">
        {showLast ? (es ? 'Último predeterminado:' : 'Last default:') : heading}
      </Typography>
      {rows.length === 0 ? (
        <Typography variant="body2" color="text.secondary">
          {es ? 'Ningún modelo cargado' : 'No model loaded'}
        </Typography>
      ) : (
        <Box component="ul" sx={{ listStyle: 'none', m: 0, p: 0 }}>
          {rows.map((row) => (
            <Box component="li" key={row.id} sx={{ display: 'flex', alignItems: 'center', gap: 0.5, minWidth: 0 }}>
              <Typography variant="body2" component="span" aria-hidden="true">
                ▸
              </Typography>
              <Typography variant="body2" component="span" sx={{ fontFamily: 'monospace', overflowWrap: 'anywhere' }}>
                {row.id}
              </Typography>
              {row.isDefault ? <StarIcon fontSize="inherit" titleAccess={es ? 'modelo predeterminado' : 'default model'} color="warning" /> : null}
              {row.last ? (
                <Typography variant="caption" color="text.secondary" component="span">
                  {es ? '(último)' : '(last)'}
                </Typography>
              ) : null}
            </Box>
          ))}
        </Box>
      )}
    </Box>
  );
}
