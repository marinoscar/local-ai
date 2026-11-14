// The node picker of the music page, state first (docs/specs/cloud-app.md section 3.11, #514): every computer with its
// music state in words (Music ready / Can load music / Too little VRAM / Offline / No music here), then the ONE next
// thing to do for the chosen one, which for "Can load music" is the "Switch to Music" button (needs
// `localai_workloads:write`). The switch is a queued command, followed until it settles.
import { Alert, Box, Button, Chip, CircularProgress, FormControlLabel, Radio, RadioGroup, Typography } from '@mui/material';
import { useEffect, useRef, useState } from 'react';

import { useT } from '../../i18n/LanguageProvider';
import { getCommand } from '../api/commands';
import type { CommandView } from '../api/types';
import { plainError } from '../doctorFix';
import { activateWorkload, type MusicNode } from './musicApi';
import { nextActionText, readinessColor, readinessText, refusalText } from './musicText';

export interface NodePickerProps {
  nodes: MusicNode[];
  selected: string | null;
  onSelect: (nodeId: string) => void;
  canSwitch: boolean;
  /** Called when a switch settled, so the page reads the nodes again. */
  onSwitched: () => void;
  commandPollMs?: number;
}

const isActive = (command: CommandView) => command.status === 'queued' || command.status === 'running';

export function NodePicker({ nodes, selected, onSelect, canSwitch, onSwitched, commandPollMs = 2000 }: NodePickerProps) {
  const t = useT();
  const node = nodes.find((candidate) => candidate.nodeId === selected) ?? null;
  const [switching, setSwitching] = useState<CommandView | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const onSwitchedRef = useRef(onSwitched);
  onSwitchedRef.current = onSwitched;

  // Follow the switch until it settles.
  useEffect(() => {
    if (!switching || !isActive(switching)) return undefined;
    const timer = setTimeout(() => {
      getCommand(switching.id)
        .then((next) => {
          setSwitching(next);
          if (!isActive(next)) {
            if (next.status !== 'succeeded') {
              const plain = plainError(next.error as { code?: string; message?: string } | null);
              setProblem(plain.code ? `${plain.fix} (${plain.code})` : plain.fix);
            }
            onSwitchedRef.current();
          }
        })
        .catch(() => undefined);
    }, commandPollMs);
    return () => clearTimeout(timer);
  }, [switching, commandPollMs]);

  const switchToMusic = async (nodeId: string) => {
    setProblem(null);
    try {
      setSwitching(await activateWorkload(nodeId, 'music'));
    } catch (error) {
      setProblem(refusalText(error, t));
    }
  };

  if (nodes.length === 0) {
    return <Alert severity="info">{t('No computer is enrolled yet. Add one in the Lab.', 'Todavía no hay computadoras inscritas. Agregue una en el Laboratorio.')}</Alert>;
  }

  return (
    <Box component="section" aria-labelledby="music-node-title" sx={{ display: 'flex', flexDirection: 'column', gap: 1 }}>
      <Typography id="music-node-title" variant="h6" component="h2">
        {t('Computer', 'Computadora')}
      </Typography>
      <RadioGroup aria-labelledby="music-node-title" value={selected ?? ''} onChange={(event) => onSelect(event.target.value)}>
        {nodes.map((candidate) => (
          <FormControlLabel
            key={candidate.nodeId}
            value={candidate.nodeId}
            control={<Radio />}
            sx={{ m: 0, minWidth: 0 }}
            label={
              <Box sx={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 1 }}>
                <span>{candidate.nodeName}</span>
                <Chip size="small" color={readinessColor(candidate.readiness)} label={readinessText(candidate.readiness, t)} data-testid={`readiness-${candidate.nodeId}`} />
              </Box>
            }
          />
        ))}
      </RadioGroup>
      {node ? (
        <Box data-testid="next-action" sx={{ display: 'flex', flexDirection: 'column', gap: 1, alignItems: 'flex-start' }}>
          <Typography>{nextActionText(node.readiness, node.nodeName, t)}</Typography>
          {node.readiness === 'can_load' ? (
            canSwitch ? (
              <Button
                variant="contained"
                disabled={switching !== null && isActive(switching)}
                onClick={() => void switchToMusic(node.nodeId)}
                startIcon={switching && isActive(switching) ? <CircularProgress size={16} aria-label={t('Switching', 'Cambiando')} /> : undefined}
              >
                {t(`Switch ${node.nodeName} to Music`, `Cambiar ${node.nodeName} a Música`)}
              </Button>
            ) : (
              <Typography color="text.secondary">{t('Ask someone who can switch workloads.', 'Pídaselo a alguien que pueda cambiar el modo de trabajo.')}</Typography>
            )
          ) : null}
          {switching && isActive(switching) ? (
            <Typography role="status" color="text.secondary">
              {switching.status === 'queued' ? t(`Waiting for ${node.nodeName}…`, `Esperando a ${node.nodeName}…`) : t('Loading music. This can take a minute…', 'Cargando la música. Puede tardar un minuto…')}
            </Typography>
          ) : null}
        </Box>
      ) : null}
      {problem ? (
        <Alert severity="error" role="alert" onClose={() => setProblem(null)}>
          {problem}
        </Alert>
      ) : null}
    </Box>
  );
}
