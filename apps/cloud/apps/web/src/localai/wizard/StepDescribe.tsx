// Wizard step 1 (docs/specs/cloud-app.md section 3.11): describe the node. Pressing Create node is the only thing that
// creates anything; the next free id is a suggestion, and an id that was ever used is refused by the server.
import { Alert, Box, Button, FormControl, FormControlLabel, FormHelperText, FormLabel, MenuItem, Radio, RadioGroup, Stack, TextField, Typography } from '@mui/material';
import { useEffect, useMemo, useState, type FormEvent } from 'react';

import { useT } from '../../i18n/LanguageProvider';
import { messageOf, reasonOf, type WizardClient } from './wizardApi';
import type { CreateNodeInput, CreateNodeResult, ServePattern } from './wizardTypes';

export const NODE_ID_RE = /^ai-n[1-9][0-9]{0,3}$/;
const DOMAIN = 'marin.cr';
/** The first octet 100 and second 64 to 127 (the Tailscale CGNAT block), or a Tailscale IPv6 address. */
const PROFILES = [
  { id: '', label: 'Not sure yet' },
  { id: 'small-8gb', label: 'small-8gb' },
  { id: 'cuda-12gb', label: 'cuda-12gb' },
  { id: 'max-128gb', label: 'max-128gb' },
  { id: 'cpu-only', label: 'cpu-only' },
];

export interface StepDescribeProps {
  client: WizardClient;
  /** Called once with the server's answer, which holds the credential. */
  onCreated: (result: CreateNodeResult) => void;
}

export function StepDescribe({ client, onCreated }: StepDescribeProps) {
  const t = useT();
  const [nodeId, setNodeId] = useState('');
  const [suggested, setSuggested] = useState<string | null>(null);
  const [displayName, setDisplayName] = useState('');
  const [publicHost, setPublicHost] = useState('');
  const [hostTouched, setHostTouched] = useState(false);
  const [servePattern, setServePattern] = useState<ServePattern>('B');
  const [tailnetMachine, setTailnetMachine] = useState('');
  const [profile, setProfile] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [idUsed, setIdUsed] = useState(false);

  // The suggestion arrives once; a field the operator already typed in is never overwritten.
  useEffect(() => {
    let alive = true;
    client
      .nextNodeId()
      .then((id) => {
        if (!alive || id === null) return;
        setSuggested(id);
        setNodeId((current) => (current === '' ? id : current));
      })
      .catch(() => undefined);
    return () => {
      alive = false;
    };
  }, [client]);

  const derivedHost = NODE_ID_RE.test(nodeId) ? `${nodeId}.${DOMAIN}` : '';
  const host = hostTouched ? publicHost : derivedHost;
  const idError = nodeId !== '' && !NODE_ID_RE.test(nodeId) ? t('Use ai-n and a number from 1 to 9999, for example ai-n4.', 'Use ai-n y un número del 1 al 9999, por ejemplo ai-n4.') : null;
  const canSubmit = useMemo(() => NODE_ID_RE.test(nodeId) && !busy, [nodeId, busy]);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!canSubmit) return;
    setBusy(true);
    setError(null);
    setIdUsed(false);
    const input: CreateNodeInput = {
      nodeId,
      servePattern,
      ...(displayName.trim() ? { displayName: displayName.trim() } : {}),
      ...(host && host !== derivedHost ? { publicHost: host.trim() } : {}),
      ...(tailnetMachine.trim() ? { tailnetMachine: tailnetMachine.trim() } : {}),
      ...(profile ? { profile } : {}),
    };
    try {
      onCreated(await client.createNode(input));
    } catch (caught) {
      if (reasonOf(caught) === 'node_id_used') {
        setIdUsed(true);
        // The list may have moved on: ask again so the button offers a free id.
        client.nextNodeId().then((id) => id && setSuggested(id)).catch(() => undefined);
      } else {
        setError(messageOf(caught, t('The node could not be created. Check the fields and try again.', 'No se pudo crear el nodo. Revise los campos e inténtelo de nuevo.')));
      }
      setBusy(false);
    }
  };

  return (
    <Box component="form" onSubmit={submit} noValidate aria-label={t('Describe the node', 'Describa el nodo')} autoComplete="off">
      <Stack spacing={2.5} sx={{ maxWidth: 640 }}>
        <Typography color="text.secondary">{t('Tell the cloud about the new computer. You can change most of this later in the node’s Settings.', 'Cuéntele a la nube sobre la computadora nueva. Casi todo se puede cambiar después en los Ajustes del nodo.')}</Typography>

        {idUsed ? (
          <Alert
            severity="warning"
            action={
              suggested ? (
                <Button color="inherit" size="small" onClick={() => { setNodeId(suggested); setIdUsed(false); }}>
                  {t('Use', 'Usar')} {suggested}
                </Button>
              ) : undefined
            }
          >
            {t('That id has been used before. Ids are never reused, even for a retired node.', 'Ese id ya se usó antes. Los ids nunca se reutilizan, ni siquiera los de un nodo retirado.')}
          </Alert>
        ) : null}
        {error ? <Alert severity="error">{error}</Alert> : null}

        <TextField
          label={t('Node id', 'Id del nodo')}
          value={nodeId}
          onChange={(event) => { setNodeId(event.target.value.trim().toLowerCase()); setIdUsed(false); }}
          error={idError !== null}
          helperText={idError ?? (suggested ? t(`Next free id suggested (${suggested}); an id is never reused.`, `Se sugiere el siguiente id libre (${suggested}); un id nunca se reutiliza.`) : t('For example ai-n4. An id is never reused.', 'Por ejemplo ai-n4. Un id nunca se reutiliza.'))}
          required
          slotProps={{ htmlInput: { autoCapitalize: 'none', spellCheck: false } }}
        />
        <TextField label={t('Name', 'Nombre')} value={displayName} onChange={(event) => setDisplayName(event.target.value)} placeholder={`Dell Precision (${nodeId || 'ai-n4'})`} helperText={t('Shown on the Lab page. Leave empty to use the node id.', 'Se muestra en la página del laboratorio. Déjelo vacío para usar el id del nodo.')} />
        <TextField
          label={t('Public host', 'Dirección pública')}
          value={host}
          onChange={(event) => { setHostTouched(true); setPublicHost(event.target.value.trim().toLowerCase()); }}
          helperText={hostTouched ? t('Overridden. Clear it to go back to the derived name.', 'Cambiada a mano. Bórrela para volver al nombre derivado.') : t('Derived from the node id.', 'Derivada del id del nodo.')}
          slotProps={{ htmlInput: { autoCapitalize: 'none', spellCheck: false } }}
        />
        <FormControl>
          <FormLabel id="serve-label">{t('Serve', 'Publicar')}</FormLabel>
          <RadioGroup aria-labelledby="serve-label" value={servePattern} onChange={(event) => setServePattern(event.target.value as ServePattern)}>
            <FormControlLabel value="B" control={<Radio />} label={`${t('Pattern', 'Patrón')} B  (tailscale serve --https=443)`} />
            <FormControlLabel value="A" control={<Radio />} label={`${t('Pattern', 'Patrón')} A  (tailscale serve --tcp=18080)`} />
          </RadioGroup>
          <FormHelperText>{t('Pattern B unless you have a reason for A.', 'Patrón B, salvo que tenga una razón para A.')}</FormHelperText>
        </FormControl>
        <TextField
          label={t('Tailnet name', 'Nombre en la tailnet')}
          value={tailnetMachine}
          onChange={(event) => setTailnetMachine(event.target.value)}
          helperText={t('Optional. The node reports its own tailnet name and address when it contacts the cloud (Part 3), so you do not need to type either.', 'Opcional. El nodo informa su propio nombre y dirección en la tailnet cuando contacta a la nube (Parte 3), así que no necesita escribir ninguno.')}
          slotProps={{ htmlInput: { autoCapitalize: 'none', spellCheck: false } }}
        />
        <TextField select label={t('Profile hint', 'Perfil sugerido')} value={profile} onChange={(event) => setProfile(event.target.value)} helperText={t('Your best guess; the installer confirms it with aictl profile suggest.', 'Su mejor estimación; el instalador la confirma con aictl profile suggest.')}>
          {PROFILES.map((option) => (
            <MenuItem key={option.id} value={option.id}>
              {option.id === '' ? t('Not sure yet', 'Todavía no estoy seguro') : option.label}
            </MenuItem>
          ))}
        </TextField>

        <Box>
          <Button type="submit" variant="contained" disabled={!canSubmit}>
            {busy ? t('Creating…', 'Creando…') : t('Create node', 'Crear nodo')}
          </Button>
        </Box>
      </Stack>
    </Box>
  );
}
