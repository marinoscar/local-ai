// What a node is set for, and one button per workload (docs/specs/cloud-app.md section 3.11, #514): the active one
// pressed, one that does not fit or misses models disabled with the reason. Props are plain data; it fetches nothing.
//
// LOCAL STAND-IN: `packages/ui` is getting a shared `WorkloadSwitcher` (#512) in parallel. Swap this one for it (with a
// cloud transport) once it lands; the props here follow the same contract (the agent's `GET /workloads` items).
import { Alert, Box, Button, CircularProgress, Stack, Typography } from '@mui/material';

import { useLanguage, useT } from '../../i18n/LanguageProvider';
import type { WorkloadItem } from './musicApi';

export interface WorkloadSwitcherProps {
  /** The node's workloads from its last snapshot; null when its agent does not report them. */
  workloads: WorkloadItem[] | null;
  active: string | null;
  /** The person holds `localai_workloads:write`. */
  canSwitch: boolean;
  /** Why no switch is possible now (Offline mode, ...), or null. */
  blockedReason?: string | null;
  /** The workload a switch is in flight to, or null. */
  busyName?: string | null;
  onSwitch: (name: string) => void;
}

export function WorkloadSwitcher({ workloads, active, canSwitch, blockedReason = null, busyName = null, onSwitch }: WorkloadSwitcherProps) {
  const t = useT();
  const { lang } = useLanguage();
  const label = (item: WorkloadItem) => (lang === 'es' ? item.label.es : item.label.en);
  const activeItem = workloads?.find((item) => item.name === active) ?? null;

  if (workloads === null) {
    return (
      <Typography color="text.secondary" data-testid="workloads-unknown">
        {t('This computer does not report its workloads yet. Update its agent.', 'Esta computadora todavía no informa sus modos de trabajo. Actualice su agente.')}
      </Typography>
    );
  }

  return (
    <Box component="section" aria-label={t('Workload', 'Modo de trabajo')} data-testid="workload-switcher">
      <Typography>
        {t('This computer is set for:', 'Esta computadora está configurada para:')}{' '}
        <strong>{activeItem ? label(activeItem) : active ?? t('nothing yet', 'nada todavía')}</strong>
      </Typography>
      {blockedReason ? (
        <Alert severity="info" sx={{ mt: 1 }}>
          {blockedReason}
        </Alert>
      ) : null}
      {canSwitch && !blockedReason ? (
        <Stack direction="row" useFlexGap spacing={1} sx={{ mt: 1, flexWrap: 'wrap' }}>
          {workloads.map((item) => {
            const isActive = item.name === active;
            const reason = item.missing.length > 0 ? t('Its models are not on this computer yet.', 'Sus modelos todavía no están en esta computadora.') : !item.fits ? t('Does not fit this graphics card.', 'No cabe en esta tarjeta gráfica.') : null;
            const busy = busyName === item.name;
            return (
              <Box key={item.name} sx={{ display: 'flex', flexDirection: 'column', minWidth: 0 }}>
                <Button
                  variant={isActive ? 'contained' : 'outlined'}
                  aria-pressed={isActive}
                  disabled={isActive || reason !== null || busyName !== null}
                  onClick={() => onSwitch(item.name)}
                  startIcon={busy ? <CircularProgress size={16} aria-label={t('Switching', 'Cambiando')} /> : undefined}
                >
                  {label(item)}
                </Button>
                {reason ? (
                  <Typography variant="caption" color="text.secondary" sx={{ maxWidth: 180 }}>
                    {reason}
                  </Typography>
                ) : null}
              </Box>
            );
          })}
        </Stack>
      ) : null}
    </Box>
  );
}
