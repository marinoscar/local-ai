// The Alerts page (`/lab/alerts`, lab:read; docs/specs/cloud-app.md section 3.11): the alerts the cloud raised about the
// nodes and the ark, most urgent first, each with its severity (icon and word), node, plain title, doctor code and the plain
// fix. A filter picks Active / Resolved / All and one node. [Ack] (lab:operate only; hidden for a viewer) stops an alert's
// repeat notifications; it does not clear it. The page lays out what the API returned and derives nothing.
import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import { Alert, Box, Button, CircularProgress, FormControl, InputLabel, MenuItem, Select, Typography, type SelectChangeEvent } from '@mui/material';
import { usePermissions } from '@marinoscar/platform-web/identity/headless';
import { formatAgo, useLang } from '@localai/ui';
import { useEffect, useState } from 'react';
import { Link as RouterLink } from 'react-router-dom';

import { api } from '../../api';
import { useT } from '../../i18n/LanguageProvider';
import { SeverityChip } from './SeverityChip';
import type { AlertDto, AlertFilters, AlertStateFilter } from './types';
import { useAlerts, type AcknowledgeAlert, type FetchAlerts } from './useAlerts';

interface NodeOption {
  nodeId: string;
  displayName: string;
}

/** The node filter's options: `GET /api/localai/nodes`, read once. */
async function defaultFetchNodeOptions(): Promise<NodeOption[]> {
  const response = await api.get<{ nodes: NodeOption[] }>('/localai/nodes');
  return response.nodes.map((node) => ({ nodeId: node.nodeId, displayName: node.displayName }));
}

export interface AlertsPageProps {
  /** Replace the transports (tests). */
  fetchAlerts?: FetchAlerts;
  acknowledgeAlert?: AcknowledgeAlert;
  fetchNodeOptions?: () => Promise<NodeOption[]>;
  /** The instant "N h ago" counts from (tests). */
  now?: Date;
}

const STATE_LABELS: Record<AlertStateFilter, [en: string, es: string]> = { active: ['Active', 'Activas'], cleared: ['Resolved', 'Resueltas'], all: ['All', 'Todas'] };

function describeAck(alert: AlertDto, now: Date, lang: 'en' | 'es'): string | null {
  if (alert.acknowledgedAt === null) return null;
  return `${lang === 'es' ? 'Reconocida' : 'Acknowledged'} ${formatAgo(alert.acknowledgedAt, now, lang) ?? ''}`.trim();
}

export function AlertsPage({ fetchAlerts, acknowledgeAlert, fetchNodeOptions = defaultFetchNodeOptions, now }: AlertsPageProps) {
  const t = useT();
  const lang = useLang();
  const [filters, setFilters] = useState<AlertFilters>({ state: 'active', nodeId: '' });
  const { data, loading, error, refresh, acknowledge } = useAlerts(filters, fetchAlerts, acknowledgeAlert);
  const { hasPermission } = usePermissions();
  // [Ack] is for lab:operate: the same permission POST /api/localai/alerts/:id/ack enforces.
  const canAck = hasPermission('lab:operate');
  const [nodes, setNodes] = useState<NodeOption[]>([]);
  const [acking, setAcking] = useState<string | null>(null);
  const [ackError, setAckError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    fetchNodeOptions().then(
      (options) => live && setNodes(options),
      () => undefined, // the filter then lists only the nodes that have alerts
    );
    return () => {
      live = false;
    };
  }, [fetchNodeOptions]);

  const clock = now ?? new Date();
  const alerts = data?.alerts ?? [];
  // A node with an alert is always selectable, even when the node list could not be read.
  const options = [...nodes];
  for (const alert of alerts) if (alert.nodeId !== null && !options.some((option) => option.nodeId === alert.nodeId)) options.push({ nodeId: alert.nodeId, displayName: alert.nodeName ?? alert.nodeId });

  const onAck = async (alert: AlertDto) => {
    setAcking(alert.id);
    setAckError(null);
    try {
      await acknowledge(alert.id);
    } catch (caught) {
      setAckError(caught instanceof Error && caught.message ? caught.message : t('Could not acknowledge the alert.', 'No se pudo reconocer la alerta.'));
    } finally {
      setAcking(null);
    }
  };

  return (
    <Box component="section" aria-labelledby="alerts-title" sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 1.5, justifyContent: 'space-between' }}>
        <Typography id="alerts-title" variant="h4" component="h1">
          {t('Alerts', 'Alertas')}
        </Typography>
        <Button component={RouterLink} to="/lab" size="small">
          {t('Back to the lab', 'Volver al laboratorio')}
        </Button>
      </Box>

      <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 2 }}>
        <FormControl size="small" sx={{ minWidth: 150 }}>
          <InputLabel id="alerts-state-label">{t('Show', 'Mostrar')}</InputLabel>
          <Select
            labelId="alerts-state-label"
            id="alerts-state"
            label={t('Show', 'Mostrar')}
            value={filters.state}
            onChange={(event: SelectChangeEvent) => setFilters((previous) => ({ ...previous, state: event.target.value as AlertStateFilter }))}
          >
            {(Object.keys(STATE_LABELS) as AlertStateFilter[]).map((state) => (
              <MenuItem key={state} value={state}>
                {t(...STATE_LABELS[state])}
              </MenuItem>
            ))}
          </Select>
        </FormControl>
        <FormControl size="small" sx={{ minWidth: 170 }}>
          <InputLabel id="alerts-node-label">{t('Node', 'Nodo')}</InputLabel>
          <Select
            labelId="alerts-node-label"
            id="alerts-node"
            label={t('Node', 'Nodo')}
            value={filters.nodeId}
            onChange={(event: SelectChangeEvent) => setFilters((previous) => ({ ...previous, nodeId: event.target.value }))}
          >
            <MenuItem value="">{t('All nodes', 'Todos los nodos')}</MenuItem>
            {options.map((option) => (
              <MenuItem key={option.nodeId} value={option.nodeId}>
                {option.nodeId}
              </MenuItem>
            ))}
          </Select>
        </FormControl>
      </Box>

      {error ? (
        <Alert
          severity={data ? 'warning' : 'error'}
          action={
            <Button color="inherit" size="small" onClick={refresh}>
              {t('Try again', 'Reintentar')}
            </Button>
          }
        >
          {data ? t('Could not refresh the alerts. Showing the last answer.', 'No se pudieron actualizar las alertas. Se muestra la última respuesta.') : t('Could not load the alerts.', 'No se pudieron cargar las alertas.')}
        </Alert>
      ) : null}
      {ackError ? (
        <Alert severity="error" onClose={() => setAckError(null)}>
          {ackError}
        </Alert>
      ) : null}

      {loading && !data ? (
        <Box sx={{ display: 'grid', placeItems: 'center', minHeight: '30vh' }}>
          <CircularProgress aria-label={t('Loading the alerts', 'Cargando las alertas')} />
        </Box>
      ) : null}

      {data && alerts.length === 0 ? (
        <Box sx={{ textAlign: 'center', py: 6 }} data-testid="alerts-empty">
          <CheckCircleIcon color="success" fontSize="large" aria-hidden />
          <Typography variant="h6" component="p">
            {filters.state === 'cleared' ? t('No resolved alerts', 'No hay alertas resueltas') : t('No alerts', 'No hay alertas')}
          </Typography>
          <Typography color="text.secondary">{filters.state === 'cleared' ? t('Nothing has been resolved yet.', 'Todavía no se ha resuelto nada.') : t('Everything the cloud watches looks fine.', 'Todo lo que vigila la nube se ve bien.')}</Typography>
        </Box>
      ) : null}

      {alerts.length > 0 ? (
        <Box component="ul" aria-label={t('Alerts', 'Alertas')} sx={{ listStyle: 'none', m: 0, p: 0, display: 'flex', flexDirection: 'column', gap: 1.5 }}>
          {alerts.map((alert) => {
            const ack = describeAck(alert, clock, lang);
            const resolved = alert.state === 'cleared';
            return (
              <Box
                component="li"
                key={alert.id}
                data-testid={`alert-${alert.id}`}
                data-state={alert.state}
                sx={{ border: 1, borderColor: 'divider', borderRadius: 1, p: 1.5, display: 'flex', flexDirection: 'column', gap: 0.75, minWidth: 0 }}
              >
                <Box sx={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 1 }}>
                  <SeverityChip severity={alert.severity} />
                  <Typography component="span" sx={{ fontWeight: 600 }}>
                    {alert.nodeId ?? t('Lab', 'Laboratorio')}
                  </Typography>
                  <Typography component="span" sx={{ flex: '1 1 14rem', minWidth: 0, overflowWrap: 'anywhere' }}>
                    {alert.title}
                  </Typography>
                  <Typography component="span" variant="body2" color="text.secondary" data-testid="alert-code">
                    {alert.code}
                  </Typography>
                  {!resolved && canAck && alert.acknowledgedAt === null ? (
                    <Button size="small" variant="outlined" disabled={acking === alert.id} onClick={() => void onAck(alert)} aria-label={`${t('Acknowledge', 'Reconocer')}: ${alert.nodeId ?? t('Lab', 'Laboratorio')} ${alert.title}`}>
                      {t('Ack', 'Reconocer')}
                    </Button>
                  ) : null}
                </Box>
                <Typography variant="body2" color="text.secondary">
                  {alert.fix}
                </Typography>
                <Typography variant="caption" color="text.secondary">
                  {resolved ? `${t('Resolved', 'Resuelta')} ${formatAgo(alert.clearedAt, clock, lang) ?? ''}` : `${t('Raised', 'Generada')} ${formatAgo(alert.firstRaisedAt, clock, lang) ?? t('a while ago', 'hace un rato')}`}
                  {ack ? ` · ${ack}` : ''}
                </Typography>
              </Box>
            );
          })}
        </Box>
      ) : null}
    </Box>
  );
}

export default AlertsPage;
