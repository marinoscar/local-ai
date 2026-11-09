import ArrowCircleUpIcon from '@mui/icons-material/ArrowCircleUp';
import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import ErrorIcon from '@mui/icons-material/Error';
import NotificationsActiveIcon from '@mui/icons-material/NotificationsActive';
import BlockIcon from '@mui/icons-material/Block';
import WarningAmberIcon from '@mui/icons-material/WarningAmber';
import { Box, Card, CardActions, CardContent, Chip, Typography } from '@mui/material';
import type { ReactElement, ReactNode } from 'react';

import { pick, useLang, type Lang } from '../lang';
import { ArkChip } from './ArkChip';
import { backendLabel, formatAgo, formatClock, shortCatalog } from './format';
import { HealthChip } from './HealthChip';
import { ModeChip } from './ModeChip';
import { ModelList } from './ModelList';
import type { ArkRoot, NodeCardDto } from './types';
import { VramBar } from './VramBar';

export interface NodeCardProps {
  node: NodeCardDto;
  /** The instant "last seen" counts from. Default: now. Pass a fixed value in tests. */
  now?: Date;
  /** IANA time zone for the sync time. Default: the viewer's own. */
  timeZone?: string;
  /** The footer buttons (the cloud passes its router link "Open"; the desktop its own). Nothing here fetches or navigates. */
  actions?: ReactNode;
}

const SYNC: Record<'ok' | 'partial' | 'failed' | 'skipped', { icon: ReactElement; color: 'success' | 'warning' | 'error' | 'default' }> = {
  ok: { icon: <CheckCircleIcon fontSize="inherit" />, color: 'success' },
  partial: { icon: <WarningAmberIcon fontSize="inherit" />, color: 'warning' },
  failed: { icon: <ErrorIcon fontSize="inherit" />, color: 'error' },
  skipped: { icon: <BlockIcon fontSize="inherit" />, color: 'default' },
};

const DRILL: Record<'pass' | 'warn' | 'fail', { icon: ReactElement; color: 'success' | 'warning' | 'error'; word: string; wordEs: string }> = {
  pass: { icon: <CheckCircleIcon fontSize="inherit" />, color: 'success', word: 'passed', wordEs: 'superada' },
  warn: { icon: <WarningAmberIcon fontSize="inherit" />, color: 'warning', word: 'warning', wordEs: 'con avisos' },
  fail: { icon: <ErrorIcon fontSize="inherit" />, color: 'error', word: 'failed', wordEs: 'fallida' },
};

/** A status phrase with an icon, coloured by the theme (never colour alone: the word is always there). */
function Status({ color, icon, children }: { color: 'success' | 'warning' | 'error' | 'default'; icon: ReactElement; children: ReactNode }) {
  return (
    <Box component="span" sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.5, color: color === 'default' ? 'text.secondary' : `${color}.main` }}>
      {icon}
      <Typography variant="body2" component="span" sx={{ color: 'text.primary' }}>
        {children}
      </Typography>
    </Box>
  );
}

const SYNC_WORD_ES = { ok: 'correcta', partial: 'parcial', failed: 'fallida', skipped: 'omitida' } as const;

function syncText(node: NodeCardDto, timeZone: string | undefined, lang: Lang): string {
  const es = lang === 'es';
  const { result, finishedAt, skipReason } = node.sync;
  if (result === null) return es ? 'nunca ejecutada' : 'never run';
  const time = formatClock(finishedAt, timeZone);
  const word = es ? SYNC_WORD_ES[result] : result;
  if (result === 'skipped') return `${word}${skipReason ? ` (${skipReason})` : ''}`;
  return time ? `${word} ${time}` : word;
}

function ArkLine({ title, subject, roots }: { title: string; subject: string; roots: readonly ArkRoot[] }) {
  const es = useLang() === 'es';
  return (
    <Box sx={{ display: 'flex', alignItems: 'flex-start', gap: 1 }}>
      <Typography variant="body2" component="span" sx={{ minWidth: '7.5em', pt: 0.25 }}>
        {title}
      </Typography>
      <Box sx={{ display: 'flex', flexDirection: 'column', gap: 0.5, minWidth: 0, alignItems: 'flex-start' }}>
        {roots.length === 0 ? (
          <Typography variant="body2" color="text.secondary">
            {es ? '— ninguno' : '— none'}
          </Typography>
        ) : (
          roots.map((root) => <ArkChip key={root.rootId} colour={root.colour} label={root.label} subject={subject} />)
        )}
      </Box>
    </Box>
  );
}

/**
 * One node of the lab: name, health and mode, hardware, loaded models (or the last default when the node is quiet),
 * VRAM, ark readiness per root, sync and drill, versions with "behind" flags, and the alert count. Props are plain data;
 * nothing fetches. The same card renders in the cloud Lab page and the desktop Lab view.
 */
export function NodeCard({ node, now = new Date(), timeZone, actions }: NodeCardProps) {
  const lang = useLang();
  const es = lang === 'es';
  const serving = node.health === 'healthy' || node.health === 'degraded';
  const lastSeen = formatAgo(node.lastStateAt, now, lang);
  const hardware = [node.backend ? backendLabel(node.backend) : null, node.hardware].filter((part): part is string => part !== null && part !== '').join(' · ');
  const internalRoots = node.ark.filter((root) => root.kind === 'internal');
  const driveRoots = node.ark.filter((root) => root.kind === 'ark_drive');
  const titleId = `node-${node.nodeId}-title`;
  const drill = node.drill.result === null ? null : DRILL[node.drill.result];
  const sync = node.sync.result === null ? SYNC.skipped : SYNC[node.sync.result];

  return (
    <Card component="article" aria-labelledby={titleId} variant="outlined" sx={{ height: '100%', display: 'flex', flexDirection: 'column' }} data-testid={`node-card-${node.nodeId}`}>
      <CardContent sx={{ display: 'flex', flexDirection: 'column', gap: 1.25, flex: 1 }}>
        <Box>
          <Typography id={titleId} variant="h6" component="h2" sx={{ overflowWrap: 'anywhere' }}>
            <Box component="span" sx={{ fontFamily: 'monospace' }}>
              {node.nodeId}
            </Box>{' '}
            <Box component="span" sx={{ fontWeight: 400 }}>
              {node.displayName}
            </Box>
          </Typography>
          <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.75, mt: 0.75 }}>
            <HealthChip health={node.health} mode={node.mode} phase={node.phase} />
            {node.health === 'quiet' && node.phase === 'pending' ? null : <ModeChip mode={node.mode} />}
          </Box>
        </Box>

        {hardware ? <Typography variant="body2">{hardware}</Typography> : null}

        {serving ? (
          <>
            <ModelList models={node.loadedModels} />
            {node.vram ? <VramBar usedMib={node.vram.usedMib} totalMib={node.vram.totalMib} /> : null}
          </>
        ) : (
          <>
            <Typography variant="body2" color="text.secondary">
              {lastSeen ? (es ? `Visto ${lastSeen}` : `Last seen ${lastSeen}`) : es ? 'Nunca visto' : 'Never seen'}
            </Typography>
            {node.lastDefault !== null ? <ModelList models={[]} lastDefault={node.lastDefault} /> : null}
          </>
        )}

        <Box sx={{ display: 'flex', flexDirection: 'column', gap: 0.5 }}>
          <ArkLine title={es ? 'Arca: interna' : 'Ark: internal'} subject={es ? 'interna' : 'internal'} roots={internalRoots} />
          <ArkLine title={es ? 'unidad' : 'drive'} subject={es ? 'unidad' : 'drive'} roots={driveRoots} />
        </Box>

        <Box sx={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 1.5 }}>
          <Typography variant="body2" component="span">
            {es ? 'Sincronización:' : 'Sync:'}
          </Typography>
          <Status color={sync.color} icon={sync.icon}>
            {syncText(node, timeZone, lang)}
          </Status>
          <Typography variant="body2" component="span">
            {es ? 'Simulacro:' : 'Drill:'}
          </Typography>
          {drill ? (
            <Status color={drill.color} icon={drill.icon}>
              {es ? drill.wordEs : drill.word}
            </Status>
          ) : (
            <Typography variant="body2" color="text.secondary" component="span">
              {es ? 'sin ejecutar' : 'not run'}
            </Typography>
          )}
        </Box>

        <Box sx={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 1.5 }} data-testid="versions">
          <Typography variant="body2" component="span">
            {node.agentVersion ? `${es ? 'agente' : 'agent'} ${node.agentVersion}` : es ? 'agente desconocido' : 'agent unknown'}
          </Typography>
          {node.agentBehind ? <Behind what="agent" /> : null}
          <Typography variant="body2" component="span">
            {node.catalogVersion ? `${es ? 'cat.' : 'cat'} ${shortCatalog(node.catalogVersion)}` : es ? 'cat. desconocido' : 'cat unknown'}
          </Typography>
          {node.catalogBehind ? <Behind what="catalog" /> : null}
        </Box>
      </CardContent>

      {actions !== undefined || node.alerts.active > 0 ? (
        <CardActions sx={{ justifyContent: 'space-between', px: 2, pb: 2, flexWrap: 'wrap', gap: 1 }}>
          <Box sx={{ display: 'flex', gap: 1 }}>{actions}</Box>
          {node.alerts.active > 0 ? (
            <Chip
              size="small"
              color={node.alerts.critical > 0 ? 'error' : 'warning'}
              icon={<NotificationsActiveIcon />}
              label={`${node.alerts.active} ${pick(lang, node.alerts.active === 1 ? 'alert' : 'alerts', node.alerts.active === 1 ? 'alerta' : 'alertas')}${node.alerts.critical > 0 ? `, ${node.alerts.critical} ${pick(lang, 'critical', node.alerts.critical === 1 ? 'crítica' : 'críticas')}` : ''}`}
              data-testid="alert-chip"
            />
          ) : null}
        </CardActions>
      ) : null}
    </Card>
  );
}

/** The "▲ update available" mark: an icon with a text alternative. */
function Behind({ what }: { what: 'agent' | 'catalog' }) {
  const es = useLang() === 'es';
  return (
    <Status color="warning" icon={<ArrowCircleUpIcon fontSize="inherit" />}>
      {what === 'agent' ? (es ? 'hay una actualización' : 'update available') : es ? 'catálogo atrasado' : 'catalog behind'}
    </Status>
  );
}
