// Ark tab (docs/specs/cloud-app.md sections 3.11 and 4.7): one panel per root (the internal store, each drive the node
// has seen) with the readiness chip the API computed, the three tier chips, what is missing / failed / skipped, how full
// the root is, when it was last synced and verified (and, for a drive, `drive_last_synced_at` as an age), and the last
// sync run with its error code and the plain fix from the doctor registry. Sync now, Preview (dry run) and Verify are
// lab:operate. The colours come from the API; nothing here recomputes readiness.
import { ArkChip, EnsureLine, ensureCanStart, ensureStepName, formatAgo, useLang } from '@localai/ui';
import { Alert, Box, Chip, LinearProgress, Paper, Typography } from '@mui/material';

import { useT } from '../../../i18n/LanguageProvider';
import type { NodeDetail, SnapshotArkRoot, SnapshotEnsure } from '../../api/types';
import { plainError } from '../../doctorFix';
import { FixText } from '../FixText';
import { formatBytes, formatWhen } from '../format';
import { CommandButton } from '../useNodeActions';
import { useNodeCommands } from '../useNodeCommands';

const TIER_COLOUR: Record<string, 'success' | 'warning' | 'error' | 'default'> = { ok: 'success', partial: 'warning', missing: 'error', not_planned: 'default' };
const TIER_WORD: Record<string, string> = { ok: 'ok', partial: 'partial', missing: 'missing', not_planned: 'not planned' };

function Tier({ name, state }: { name: string; state: string }) {
  return <Chip size="small" variant={state === 'ok' ? 'filled' : 'outlined'} color={TIER_COLOUR[state] ?? 'default'} label={`${name} ${TIER_WORD[state] ?? state}`} />;
}

function RootPanel({ node, rootId, kind, present, colour, label, driveAgeDays, root, driveLastSyncedAt, now }: {
  node: NodeDetail;
  rootId: string;
  kind: 'internal' | 'ark_drive';
  present: boolean;
  colour: NodeDetail['ark'][number]['colour'];
  label: string;
  driveAgeDays: number | null;
  root: SnapshotArkRoot | undefined;
  driveLastSyncedAt: string | null;
  now: Date;
}) {
  const used = root?.bytes_used ?? 0;
  const free = root?.bytes_free ?? 0;
  const percent = used + free > 0 ? Math.round((used / (used + free)) * 100) : 0;
  const title = kind === 'internal' ? 'Internal store' : present ? `Drive ${rootId}` : 'Ark drive (not plugged in)';
  return (
    <Paper variant="outlined" component="section" aria-label={title} data-testid={`ark-root-${rootId}`} sx={{ p: 2, display: 'flex', flexDirection: 'column', gap: 1.5 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap' }}>
        <Typography variant="h6" component="h2">
          {title}
        </Typography>
        <ArkChip colour={colour} label={label} subject={kind === 'internal' ? 'internal' : 'drive'} />
      </Box>

      {root ? (
        <>
          <Box sx={{ display: 'flex', gap: 0.75, flexWrap: 'wrap' }} aria-label="Tiers">
            <Tier name="Essential (t1)" state={root.tiers.t1} />
            <Tier name="Useful (t2)" state={root.tiers.t2} />
            <Tier name="Nice to have (t3)" state={root.tiers.t3} />
          </Box>
          <Typography variant="body2" data-testid="ark-items">
            Items: {root.items_missing} missing · {root.items_failed} failed · {root.items_skipped_budget} skipped (no room)
          </Typography>
          <Box>
            <LinearProgress variant="determinate" value={percent} aria-label={`Space used on ${title}: ${formatBytes(used)} of ${formatBytes(used + free)}`} sx={{ height: 8, borderRadius: 4 }} />
            <Typography variant="caption" color="text.secondary">
              {formatBytes(used)} used · {formatBytes(free)} free
            </Typography>
          </Box>
          <Typography variant="body2">
            Last sync: {formatWhen(root.last_sync_at)}
            {root.last_sync_at ? ` (${formatAgo(root.last_sync_at, now)})` : ''} · Last check: {formatWhen(root.last_verify_at)}
            {root.last_verify_at ? ` (${formatAgo(root.last_verify_at, now)})` : ''}
          </Typography>
        </>
      ) : (
        <Typography variant="body2" color="text.secondary">
          {kind === 'ark_drive' ? 'The drive is not plugged in, so there are no details.' : 'No details reported yet.'}
        </Typography>
      )}
      {kind === 'ark_drive' ? (
        <Typography variant="body2" data-testid="ark-drive-age">
          Drive last synced: {formatWhen(driveLastSyncedAt)}
          {driveAgeDays !== null ? ` (${driveAgeDays} day${driveAgeDays === 1 ? '' : 's'} ago)` : ''}
        </Typography>
      ) : null}
    </Paper>
  );
}

const STEP_STATUS: Record<SnapshotEnsure['steps'][number]['status'], [string, string]> = {
  ok: ['already installed', 'ya instalado'],
  pending: ['next in line', 'en espera de turno'],
  waiting: ['waiting for the button', 'espera el botón'],
  needs_confirm: ['needs a person at the computer', 'necesita a una persona en la computadora'],
  blocked: ['cannot be done yet (the kit lacks it)', 'aún no se puede (falta en el kit)'],
  running: ['installing…', 'instalando…'],
  done: ['installed', 'instalado'],
  failed: ['failed', 'falló'],
  cancelled: ['stopped', 'detenido'],
};

/**
 * "Install what's missing" (ADR-0010, issue #480): the state of what the node installs from its own kit FIRST (Complete,
 * Installing n of m, Needs you: one thing), the button that asks the agent to install it (`ensure.run`, lab:operate, the safe
 * default only: compiling and restarting a running service are never started from here), and each part in plain words.
 */
function EnsurePanel({ node }: { node: NodeDetail }) {
  const commands = useNodeCommands();
  const t = useT();
  const lang = useLang();
  const ensure = node.ensure;
  const block = node.lastState?.ensure;
  if (!ensure) return null;
  const parts = (block?.steps ?? []).filter((step) => step.status !== 'ok');
  const waitsForAPerson = ensure.state === 'needs_you' && ensure.nextKind === 'confirm';
  return (
    <Paper variant="outlined" component="section" aria-label={t("Install what's missing", 'Instalar lo que falta')} data-testid="ark-ensure" sx={{ p: 2, display: 'flex', flexDirection: 'column', gap: 1 }}>
      <EnsureLine ensure={ensure} testId="ark-ensure-line" />
      {ensureCanStart(ensure) ? (
        <Box>
          <CommandButton node={node} type="ensure.run" variant="contained" onPress={() => void commands.send('ensure.run', {})}>
            {t("Install what's missing", 'Instalar lo que falta')}
          </CommandButton>
        </Box>
      ) : null}
      {waitsForAPerson ? (
        <Alert severity="info" data-testid="ark-ensure-person">
          {t(
            'Compiling and restarting a running service need a person at the computer: the desktop app, with the administrator PIN, or aictl ensure --yes. They cannot be started from here.',
            'Compilar y reiniciar un servicio en marcha necesitan a una persona en la computadora: la aplicación de escritorio, con el PIN de administrador, o aictl ensure --yes. No se pueden iniciar desde aquí.',
          )}
        </Alert>
      ) : null}
      {ensure.auto !== 'on' ? (
        <Typography variant="body2" color="text.secondary">
          {ensure.auto === 'ask'
            ? t('The node asks before installing (ensure.auto: ask).', 'El nodo pregunta antes de instalar (ensure.auto: ask).')
            : t('The node does not install by itself (ensure.auto: off).', 'El nodo no instala por sí solo (ensure.auto: off).')}
        </Typography>
      ) : null}
      {parts.length > 0 ? (
        <Box component="ul" sx={{ m: 0, pl: 2.5 }} data-testid="ark-ensure-parts">
          {parts.map((step) => (
            <li key={step.id}>
              <Typography variant="body2" component="span">
                {ensureStepName(step.id, lang)} · {lang === 'es' ? STEP_STATUS[step.status][1] : STEP_STATUS[step.status][0]}
              </Typography>
            </li>
          ))}
        </Box>
      ) : null}
    </Paper>
  );
}

export function ArkTab({ node, now = new Date() }: { node: NodeDetail; now?: Date }) {
  const commands = useNodeCommands();
  const roots = node.lastState?.ark?.roots ?? [];
  const lastRun = node.lastState?.sync?.last_run ?? null;
  const problem = lastRun && (lastRun.result === 'failed' || lastRun.result === 'partial') ? plainError({ code: lastRun.error_code }) : null;
  const driveLastSyncedAt = node.lastState?.ark?.drive_last_synced_at ?? null;
  const syncing = node.lastState?.sync?.running === true;

  return (
    <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
      <EnsurePanel node={node} />
      <Box sx={{ display: 'flex', gap: 1, flexWrap: 'wrap', alignItems: 'center' }}>
        <CommandButton node={node} type="ark.sync" variant="contained" onPress={() => void commands.send('ark.sync', { dry_run: false })}>
          Sync now
        </CommandButton>
        <CommandButton node={node} type="ark.sync" onPress={() => void commands.send('ark.sync', { dry_run: true })}>
          Preview (dry run)
        </CommandButton>
        <CommandButton node={node} type="ark.verify" onPress={() => void commands.send('ark.verify', {})}>
          Verify
        </CommandButton>
        {syncing ? (
          <Typography variant="body2" color="text.secondary">
            A sync is running{node.lastState?.sync?.progress_pct != null ? ` (${Math.round(node.lastState.sync.progress_pct)}%)` : ''}.
          </Typography>
        ) : null}
      </Box>

      <Box component="section" aria-label="Last sync" data-testid="ark-last-sync">
        {lastRun ? (
          <>
            <Typography variant="body2">
              Last sync run: <strong>{lastRun.result}</strong> · {formatWhen(lastRun.finished_at)} ({formatAgo(lastRun.finished_at, now)})
              {lastRun.skip_reason ? ` · skipped: ${lastRun.skip_reason}` : ''}
            </Typography>
            {problem ? (
              <Alert severity={lastRun.result === 'failed' ? 'error' : 'warning'} sx={{ mt: 1 }} data-testid="ark-sync-problem">
                {problem.meaning ? `${problem.meaning}. ` : ''}
                <FixText text={problem.fix} />
                {problem.code ? ` (${problem.code})` : ''}
              </Alert>
            ) : null}
          </>
        ) : syncing ? null : (
          // A running first sync has no finished run yet (`sync.last_run` is empty): "A sync is running" above says it, and
          // "No sync has run" would contradict it.
          <Typography variant="body2" color="text.secondary">
            No sync has run on this node yet.
          </Typography>
        )}
      </Box>

      {node.ark.length === 0 ? <Alert severity="info">{node.nodeId} has not reported its ark yet.</Alert> : null}
      <Box sx={{ display: 'grid', gap: 2, gridTemplateColumns: { xs: '1fr', md: 'repeat(2, 1fr)' } }}>
        {node.ark.map((card) => (
          <RootPanel
            key={card.rootId}
            node={node}
            rootId={card.rootId}
            kind={card.kind}
            present={card.present}
            colour={card.colour}
            label={card.label}
            driveAgeDays={card.driveAgeDays}
            root={roots.find((root) => root.id === card.rootId)}
            driveLastSyncedAt={driveLastSyncedAt}
            now={now}
          />
        ))}
      </Box>
    </Box>
  );
}
