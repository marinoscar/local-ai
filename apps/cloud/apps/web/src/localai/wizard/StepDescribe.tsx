// Wizard step 1 (docs/specs/cloud-app.md section 3.11): describe the node. Pressing Create node is the only thing that
// creates anything; the next free id is a suggestion, and an id that was ever used is refused by the server.
import { Alert, Box, Button, FormControl, FormControlLabel, FormHelperText, FormLabel, MenuItem, Radio, RadioGroup, Stack, TextField, Typography } from '@mui/material';
import { useEffect, useMemo, useState, type FormEvent } from 'react';

import { messageOf, reasonOf, type WizardClient } from './wizardApi';
import type { CreateNodeInput, CreateNodeResult, ServePattern } from './wizardTypes';

export const NODE_ID_RE = /^ai-n[1-9][0-9]{0,3}$/;
const DOMAIN = 'marin.cr';
/** The first octet 100 and second 64 to 127 (the Tailscale CGNAT block), or a Tailscale IPv6 address. */
const TAILNET_IP_RE = /^(?:100\.(?:6[4-9]|[7-9]\d|1[01]\d|12[0-7])\.\d{1,3}\.\d{1,3}|fd7a:115c:a1e0(?::[0-9a-fA-F]{1,4}){1,5})$/;
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
  const [nodeId, setNodeId] = useState('');
  const [suggested, setSuggested] = useState<string | null>(null);
  const [displayName, setDisplayName] = useState('');
  const [publicHost, setPublicHost] = useState('');
  const [hostTouched, setHostTouched] = useState(false);
  const [servePattern, setServePattern] = useState<ServePattern>('B');
  const [tailnetMachine, setTailnetMachine] = useState('');
  const [tailnetIp, setTailnetIp] = useState('');
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
  const idError = nodeId !== '' && !NODE_ID_RE.test(nodeId) ? 'Use ai-n and a number from 1 to 9999, for example ai-n4.' : null;
  const ipError = tailnetIp !== '' && !TAILNET_IP_RE.test(tailnetIp.trim()) ? 'This is not a Tailscale address (it starts with 100.64 to 100.127).' : null;
  const canSubmit = useMemo(() => NODE_ID_RE.test(nodeId) && ipError === null && !busy, [nodeId, ipError, busy]);

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
      ...(tailnetIp.trim() ? { tailnetIp: tailnetIp.trim() } : {}),
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
        setError(messageOf(caught, 'The node could not be created. Check the fields and try again.'));
      }
      setBusy(false);
    }
  };

  return (
    <Box component="form" onSubmit={submit} noValidate aria-label="Describe the node" autoComplete="off">
      <Stack spacing={2.5} sx={{ maxWidth: 640 }}>
        <Typography color="text.secondary">Tell the cloud about the new computer. You can change most of this later in the node&rsquo;s Settings.</Typography>

        {idUsed ? (
          <Alert
            severity="warning"
            action={
              suggested ? (
                <Button color="inherit" size="small" onClick={() => { setNodeId(suggested); setIdUsed(false); }}>
                  Use {suggested}
                </Button>
              ) : undefined
            }
          >
            That id has been used before. Ids are never reused, even for a retired node.
          </Alert>
        ) : null}
        {error ? <Alert severity="error">{error}</Alert> : null}

        <TextField
          label="Node id"
          value={nodeId}
          onChange={(event) => { setNodeId(event.target.value.trim().toLowerCase()); setIdUsed(false); }}
          error={idError !== null}
          helperText={idError ?? (suggested ? `Next free id suggested (${suggested}); an id is never reused.` : 'For example ai-n4. An id is never reused.')}
          required
          slotProps={{ htmlInput: { autoCapitalize: 'none', spellCheck: false } }}
        />
        <TextField label="Name" value={displayName} onChange={(event) => setDisplayName(event.target.value)} placeholder={`Dell Precision (${nodeId || 'ai-n4'})`} helperText="Shown on the Lab page. Leave empty to use the node id." />
        <TextField
          label="Public host"
          value={host}
          onChange={(event) => { setHostTouched(true); setPublicHost(event.target.value.trim().toLowerCase()); }}
          helperText={hostTouched ? 'Overridden. Clear it to go back to the derived name.' : 'Derived from the node id.'}
          slotProps={{ htmlInput: { autoCapitalize: 'none', spellCheck: false } }}
        />
        <FormControl>
          <FormLabel id="serve-label">Serve</FormLabel>
          <RadioGroup aria-labelledby="serve-label" value={servePattern} onChange={(event) => setServePattern(event.target.value as ServePattern)}>
            <FormControlLabel value="B" control={<Radio />} label="Pattern B  (tailscale serve --https=443)" />
            <FormControlLabel value="A" control={<Radio />} label="Pattern A  (tailscale serve --tcp=18080)" />
          </RadioGroup>
          <FormHelperText>Pattern B unless you have a reason for A.</FormHelperText>
        </FormControl>
        <TextField
          label="Tailnet name"
          value={tailnetMachine}
          onChange={(event) => setTailnetMachine(event.target.value)}
          helperText="The machine name, for example dell-linux. Paste the full name (dell-linux.<tailnet>.ts.net) to have it filled into the vhost file."
          slotProps={{ htmlInput: { autoCapitalize: 'none', spellCheck: false } }}
        />
        <TextField
          label="Tailnet IP"
          value={tailnetIp}
          onChange={(event) => setTailnetIp(event.target.value)}
          error={ipError !== null}
          helperText={ipError ?? 'Optional now. Stored, shown to administrators only. Without it the vhost file keeps a placeholder.'}
          slotProps={{ htmlInput: { inputMode: 'decimal', autoCapitalize: 'none', spellCheck: false } }}
        />
        <TextField select label="Profile hint" value={profile} onChange={(event) => setProfile(event.target.value)} helperText="Your best guess; the installer confirms it with aictl profile suggest.">
          {PROFILES.map((option) => (
            <MenuItem key={option.id} value={option.id}>
              {option.label}
            </MenuItem>
          ))}
        </TextField>

        <Box>
          <Button type="submit" variant="contained" disabled={!canSubmit}>
            {busy ? 'Creating…' : 'Create node'}
          </Button>
        </Box>
      </Stack>
    </Box>
  );
}
