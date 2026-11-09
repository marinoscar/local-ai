// Wizard step 3: the VPS publish checklist (docs/specs/cloud-app.md section 4.8). Phase 1 publishing is manual: the
// rows mirror runbook 05 part 7, with the commands and files the server rendered for this node. "Publish automatically"
// runs the VPS helper's publish job (section 4.10) and lets the checklist below show steps 2 to 5 as it left them.
import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import ErrorIcon from '@mui/icons-material/Error';
import RadioButtonUncheckedIcon from '@mui/icons-material/RadioButtonUnchecked';
import { Alert, Box, Button, Chip, CircularProgress, Collapse, List, ListItem, Stack, Typography } from '@mui/material';
import { useState, type ReactElement } from 'react';

import { useT } from '../../i18n/LanguageProvider';
import { CodeBlock } from './CodeBlock';
import { CopyButton } from './CopyButton';
import { messageOf, reasonOf, type WizardClient } from './wizardApi';
import type { Checklist, ChecklistStep, StepState } from './wizardTypes';
import { usePolled } from './usePolled';
import { VpsPublishPanel } from './VpsPublishPanel';

export const CHECKLIST_POLL_MS = 5000;

const STATE_CHIP: Record<StepState, { label: string; labelEs: string; color: 'default' | 'success' | 'error'; icon: ReactElement }> = {
  todo: { label: 'To do', labelEs: 'Por hacer', color: 'default', icon: <RadioButtonUncheckedIcon /> },
  done: { label: 'Done', labelEs: 'Hecho', color: 'success', icon: <CheckCircleIcon /> },
  failed: { label: 'Failed', labelEs: 'Fallido', color: 'error', icon: <ErrorIcon /> },
};

const PLACEHOLDER_HELP: Record<string, [en: string, es: string]> = {
  tailnetIp: [
    'The node has not reported its tailnet address yet, so the file shows a placeholder. It fills in by itself once the node has contacted the cloud; if the node is old, set it in the node’s Settings tab.',
    'El nodo todavía no ha informado su dirección en la tailnet, así que el archivo muestra un marcador. Se completa solo cuando el nodo contacta a la nube; si el nodo es antiguo, indíquela en la pestaña Ajustes del nodo.',
  ],
  tailnetName: [
    'The node has not reported its full tailnet name yet, so the file shows <TAILNET>. It fills in by itself once the node has contacted the cloud; if the node is old, set it in the node’s Settings tab.',
    'El nodo todavía no ha informado su nombre completo en la tailnet, así que el archivo muestra <TAILNET>. Se completa solo cuando el nodo contacta a la nube; si el nodo es antiguo, indíquelo en la pestaña Ajustes del nodo.',
  ],
};

function showLabel(step: ChecklistStep, t: (en: string, es: string) => string): string {
  return step.snippets.some((snippet) => snippet.path || snippet.language === 'nginx') ? t('Show file', 'Mostrar archivo') : t('Show command', 'Mostrar comando');
}

function StepRow({ step, busy, error, onAction }: { step: ChecklistStep; busy: boolean; error: string | null; onAction: (action: 'mark_done' | 'mark_todo' | 'check') => void }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const chip = { ...STATE_CHIP[step.state], label: t(STATE_CHIP[step.state].label, STATE_CHIP[step.state].labelEs) };
  const detailId = `step-${step.step}-detail`;
  return (
    <ListItem
      divider
      data-testid={`step-${step.step}`}
      sx={{ display: 'block', py: 1.5, px: 0 }}
      aria-label={`${step.order}. ${step.title}: ${chip.label}`}
    >
      <Stack direction={{ xs: 'column', sm: 'row' }} spacing={1} sx={{ alignItems: { sm: 'center' }, justifyContent: 'space-between' }}>
        <Stack direction="row" spacing={1.25} sx={{ alignItems: 'center', minWidth: 0 }}>
          <Chip size="small" icon={chip.icon} color={chip.color} label={chip.label} variant={step.state === 'todo' ? 'outlined' : 'filled'} />
          <Typography sx={{ minWidth: 0, overflowWrap: 'anywhere' }}>
            <strong>{step.order}.</strong> {step.title}
          </Typography>
        </Stack>
        <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: 'wrap' }}>
          {step.kind === 'auto' ? (
            <Typography variant="body2" color="text.secondary" sx={{ alignSelf: 'center' }}>
              {t('(automatic)', '(automático)')}
            </Typography>
          ) : null}
          {step.kind === 'check' ? (
            <Button size="small" variant="outlined" disabled={busy} onClick={() => onAction('check')} startIcon={busy ? <CircularProgress size={14} /> : undefined}>
              {t('Check', 'Comprobar')}
            </Button>
          ) : null}
          {step.kind === 'manual' ? (
            step.state === 'done' ? (
              <Button size="small" variant="outlined" disabled={busy} onClick={() => onAction('mark_todo')} aria-label={t(`Mark step ${step.order} as to do`, `Marcar el paso ${step.order} como por hacer`)}>
                {t('Mark as to do', 'Marcar como por hacer')}
              </Button>
            ) : (
              <Button size="small" variant="outlined" disabled={busy} onClick={() => onAction('mark_done')} aria-label={t(`Mark step ${step.order} done`, `Marcar el paso ${step.order} como hecho`)}>
                {t('Mark done', 'Marcar como hecho')}
              </Button>
            )
          ) : null}
          {step.snippets.length > 0 ? (
            <Button size="small" onClick={() => setOpen((value) => !value)} aria-expanded={open} aria-controls={`step-${step.step}-snippets`}>
              {open ? t('Hide', 'Ocultar') : showLabel(step, t)}
            </Button>
          ) : null}
        </Stack>
      </Stack>
      {step.detail ? (
        <Typography id={detailId} variant="body2" color={step.state === 'failed' ? 'error' : 'text.secondary'} sx={{ mt: 0.75, ml: { sm: 0.5 }, overflowWrap: 'anywhere' }}>
          {step.detail}
        </Typography>
      ) : null}
      {error ? (
        <Alert severity="error" sx={{ mt: 1 }}>
          {error}
        </Alert>
      ) : null}
      {step.snippets.length > 0 ? (
        <Collapse in={open} unmountOnExit>
          <Stack id={`step-${step.step}-snippets`} spacing={1.5} sx={{ mt: 1.5 }}>
            {step.missing.map((missing) => (
              <Alert key={missing} severity="info">
                {t(...(PLACEHOLDER_HELP[missing] ?? ['', '']))}
              </Alert>
            ))}
            {step.snippets.map((snippet, index) => (
              <Box key={index}>
                <Typography variant="body2" sx={{ mb: 0.5 }}>
                  {snippet.title}
                  {snippet.path ? (
                    <>
                      {' '}
                      <Box component="code" sx={{ wordBreak: 'break-all' }}>
                        {snippet.path}
                      </Box>
                    </>
                  ) : null}
                </Typography>
                <CodeBlock label={snippet.path ? `${t('File', 'Archivo')} ${snippet.path}` : snippet.title}>{snippet.content}</CodeBlock>
                <Box sx={{ mt: 0.75 }}>
                  <CopyButton text={snippet.content} label={t('Copy', 'Copiar')} context={snippet.title} />
                </Box>
              </Box>
            ))}
          </Stack>
        </Collapse>
      ) : null}
    </ListItem>
  );
}

export interface StepPublishProps {
  client: WizardClient;
  nodeId: string;
}

export function StepPublish({ client, nodeId }: StepPublishProps) {
  const t = useT();
  const poll = usePolled<Checklist>(() => client.checklist(nodeId), CHECKLIST_POLL_MS);
  const [busyStep, setBusyStep] = useState<string | null>(null);
  const [rowError, setRowError] = useState<{ step: string; message: string } | null>(null);
  const checklist = poll.data;

  const run = async (step: ChecklistStep, action: 'mark_done' | 'mark_todo' | 'check') => {
    setBusyStep(step.step);
    setRowError(null);
    try {
      poll.set(await client.act(nodeId, step.step, action));
    } catch (caught) {
      const reason = reasonOf(caught);
      setRowError({ step: step.step, message: reason === 'node_retired' ? t('This node is retired.', 'Este nodo está retirado.') : messageOf(caught, t('That did not work. Try again.', 'Eso no funcionó. Inténtelo de nuevo.')) });
    } finally {
      setBusyStep(null);
    }
  };

  return (
    <Stack spacing={2} sx={{ maxWidth: 900 }}>
      <Typography color="text.secondary" sx={{ maxWidth: 560 }}>
        {t(`Make ${nodeId} reachable from the internet through the VPS. These are the steps of the “Add a node” runbook (Part 7); run the VPS commands as root on the VPS, or let the VPS helper do steps 2 to 5.`, `Haga que ${nodeId} sea accesible desde internet a través del VPS. Son los pasos del manual «Agregar un nodo» (Parte 7); ejecute los comandos del VPS como root en el VPS, o deje que el ayudante del VPS haga los pasos 2 a 5.`)}
      </Typography>

      <VpsPublishPanel client={client} nodeId={nodeId} onSettled={poll.refresh} />

      {checklist?.complete ? (
        <Alert severity="success" role="status" data-testid="lab-banner">
          {t(`${nodeId} is part of the lab`, `${nodeId} ya forma parte del laboratorio`)}
        </Alert>
      ) : null}

      {poll.error ? (
        <Alert severity={checklist ? 'warning' : 'error'} action={<Button color="inherit" size="small" onClick={poll.refresh}>{t('Try again', 'Reintentar')}</Button>}>
          {checklist ? t('Could not refresh the checklist. Showing the last answer.', 'No se pudo actualizar la lista. Se muestra la última respuesta.') : t('Could not load the checklist.', 'No se pudo cargar la lista.')}
        </Alert>
      ) : null}

      {poll.loading && !checklist ? (
        <Box sx={{ display: 'grid', placeItems: 'center', minHeight: '20vh' }}>
          <CircularProgress aria-label={t('Loading the checklist', 'Cargando la lista')} />
        </Box>
      ) : null}

      {checklist ? (
        <List aria-label={t('Publish checklist', 'Lista para publicar')} disablePadding>
          {checklist.steps.map((step) => (
            <StepRow key={step.step} step={step} busy={busyStep === step.step} error={rowError?.step === step.step ? rowError.message : null} onAction={(action) => void run(step, action)} />
          ))}
        </List>
      ) : null}
    </Stack>
  );
}
