// Settings tab (docs/specs/cloud-app.md sections 3.9 and 3.11): display name, public host, serve pattern, tailnet
// machine, tailnet IP, gateway enabled / priority, catalog pin, and **Retire node…**. Everyone with lab:read sees the
// values; only lab:admin sees the controls (edits go through `PATCH /api/localai/nodes/:nodeId`).
//
// The tailnet IP is never on the page until an admin asks: it shows masked (`100.•••.•••.•••`) and **Reveal** reads
// `?reveal=tailnet`, which the API audits as `localai.node.tailnet_revealed`. The revealed value lives only in this
// component's state; **Hide** and leaving the tab drop it. It is never logged or stored by the page.
import { ApiError } from '@marinoscar/platform-web/core';
import { Alert, Box, Button, FormControlLabel, MenuItem, Paper, Stack, Switch, TextField, Typography } from '@mui/material';
import { useState, type ReactNode } from 'react';
import { useNavigate } from 'react-router-dom';

import { patchNode, retireNode, revealTailnetIp } from '../../api/nodes';
import type { NodeDetail, PatchNodeInput, ServePattern } from '../../api/types';
import { TypedConfirmDialog } from '../ConfirmDialog';
import { useMay } from '../useNodeActions';

export const MASKED_TAILNET_IP = '100.•••.•••.•••';

interface Draft {
  displayName: string;
  publicHost: string;
  servePattern: ServePattern;
  tailnetMachine: string;
  gatewayEnabled: boolean;
  gatewayPriority: string;
  catalogPin: string;
}

const draftOf = (node: NodeDetail): Draft => ({
  displayName: node.displayName,
  publicHost: node.settings.publicHost,
  servePattern: node.settings.servePattern,
  tailnetMachine: node.settings.tailnetMachine ?? '',
  gatewayEnabled: node.settings.gatewayEnabled,
  gatewayPriority: String(node.settings.gatewayPriority),
  catalogPin: node.settings.catalogPin ?? '',
});

/** The fields that differ from the node, in the PATCH body's shape. */
export function changesOf(node: NodeDetail, draft: Draft): PatchNodeInput {
  const before = draftOf(node);
  const body: PatchNodeInput = {};
  if (draft.displayName !== before.displayName) body.displayName = draft.displayName;
  if (draft.publicHost !== before.publicHost) body.publicHost = draft.publicHost;
  if (draft.servePattern !== before.servePattern) body.servePattern = draft.servePattern;
  if (draft.tailnetMachine !== before.tailnetMachine) body.tailnetMachine = draft.tailnetMachine.trim() === '' ? null : draft.tailnetMachine;
  if (draft.gatewayEnabled !== before.gatewayEnabled) body.gatewayEnabled = draft.gatewayEnabled;
  if (draft.gatewayPriority !== before.gatewayPriority) body.gatewayPriority = Number(draft.gatewayPriority);
  if (draft.catalogPin !== before.catalogPin) body.catalogPin = draft.catalogPin.trim() === '' ? null : draft.catalogPin;
  return body;
}

/** `{ field: message }` from the API's 400 issues, plus a general message. */
function problemsOf(error: unknown): { fields: Record<string, string>; general: string | null } {
  if (error instanceof ApiError) {
    const issues = ((error.details ?? {}) as { issues?: Array<{ path?: string; message?: string }> }).issues;
    if (Array.isArray(issues) && issues.length > 0) {
      const fields: Record<string, string> = {};
      for (const issue of issues) if (issue.path) fields[issue.path] = issue.message ?? 'Not valid';
      return { fields, general: Object.keys(fields).length ? null : error.message };
    }
    return { fields: {}, general: error.status === 403 ? 'You do not have permission to change this node.' : error.message };
  }
  return { fields: {}, general: 'Could not reach the cloud. Check your connection and try again.' };
}

const SERVE_WORD: Record<ServePattern, string> = { A: 'Pattern A (--tcp)', B: 'Pattern B (--https=443)', none: 'Not published' };

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', sm: '12rem 1fr' }, gap: { xs: 0, sm: 2 }, py: 0.75, borderBottom: 1, borderColor: 'divider' }}>
      <Typography variant="body2" color="text.secondary" component="dt">
        {label}
      </Typography>
      <Typography variant="body2" component="dd" sx={{ m: 0, overflowWrap: 'anywhere' }}>
        {children}
      </Typography>
    </Box>
  );
}

export function SettingsTab({ node, onChanged }: { node: NodeDetail; onChanged: () => void }) {
  const may = useMay();
  const navigate = useNavigate();
  const [draft, setDraft] = useState<Draft | null>(null);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [problems, setProblems] = useState<{ fields: Record<string, string>; general: string | null }>({ fields: {}, general: null });
  const [revealed, setRevealed] = useState<string | null>(null);
  const [revealing, setRevealing] = useState(false);
  const [revealError, setRevealError] = useState<string | null>(null);
  const [ipEditing, setIpEditing] = useState(false);
  const [ipValue, setIpValue] = useState('');
  const [retireOpen, setRetireOpen] = useState(false);

  const shown = draft ?? draftOf(node);
  const changes = draft ? changesOf(node, draft) : {};
  const dirty = Object.keys(changes).length > 0;
  const edit = <K extends keyof Draft>(key: K, value: Draft[K]) => {
    setSaved(false);
    setDraft({ ...(draft ?? draftOf(node)), [key]: value });
  };

  const save = async (body: PatchNodeInput): Promise<boolean> => {
    setSaving(true);
    setProblems({ fields: {}, general: null });
    try {
      await patchNode(node.nodeId, body);
      setSaved(true);
      onChanged();
      return true;
    } catch (error) {
      setProblems(problemsOf(error));
      return false;
    } finally {
      setSaving(false);
    }
  };

  const reveal = async () => {
    setRevealing(true);
    setRevealError(null);
    try {
      setRevealed(await revealTailnetIp(node.nodeId));
    } catch (error) {
      setRevealError(error instanceof ApiError && error.status === 403 ? 'Only a lab administrator can reveal the tailnet IP.' : 'Could not reveal the tailnet IP. Try again.');
    } finally {
      setRevealing(false);
    }
  };

  const field = (name: keyof Draft) => ({ error: Boolean(problems.fields[name]), helperText: problems.fields[name] });

  return (
    <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
      {may.admin ? (
        <Paper variant="outlined" component="form" aria-label="Edit node settings" sx={{ p: 2 }} onSubmit={(event) => { event.preventDefault(); if (dirty) void save(changes).then((ok) => ok && setDraft(null)); }}>
          <Stack spacing={2}>
            {problems.general ? <Alert severity="error">{problems.general}</Alert> : null}
            {saved && !dirty ? <Alert severity="success">Saved.</Alert> : null}
            <TextField label="Display name" value={shown.displayName} onChange={(event) => edit('displayName', event.target.value)} size="small" {...field('displayName')} />
            <TextField label="Public host" value={shown.publicHost} onChange={(event) => edit('publicHost', event.target.value)} size="small" {...field('publicHost')} />
            <TextField select label="Serve pattern" value={shown.servePattern} onChange={(event) => edit('servePattern', event.target.value as ServePattern)} size="small" {...field('servePattern')}>
              {(['A', 'B', 'none'] as const).map((pattern) => (
                <MenuItem key={pattern} value={pattern}>
                  {SERVE_WORD[pattern]}
                </MenuItem>
              ))}
            </TextField>
            <TextField label="Tailnet machine" value={shown.tailnetMachine} onChange={(event) => edit('tailnetMachine', event.target.value)} size="small" {...field('tailnetMachine')} />
            <FormControlLabel control={<Switch checked={shown.gatewayEnabled} onChange={(event) => edit('gatewayEnabled', event.target.checked)} />} label="Gateway enabled" />
            <TextField
              label="Gateway priority"
              type="number"
              value={shown.gatewayPriority}
              onChange={(event) => edit('gatewayPriority', event.target.value)}
              size="small"
              helperText={problems.fields.gatewayPriority ?? 'A whole number; lower is tried first.'}
              error={Boolean(problems.fields.gatewayPriority)}
              slotProps={{ htmlInput: { min: 0, max: 10000, step: 1 } }}
            />
            <TextField label="Catalog pin" value={shown.catalogPin} onChange={(event) => edit('catalogPin', event.target.value)} size="small" placeholder="Follow the approved catalog" {...(problems.fields.catalogPin ? field('catalogPin') : { helperText: 'Leave empty to follow the approved catalog.' })} />
            <Box>
              <Button type="submit" variant="contained" disabled={!dirty || saving}>
                Save changes
              </Button>
              {dirty ? (
                <Button sx={{ ml: 1 }} onClick={() => { setDraft(null); setProblems({ fields: {}, general: null }); }} disabled={saving}>
                  Discard
                </Button>
              ) : null}
            </Box>
          </Stack>
        </Paper>
      ) : (
        <Paper variant="outlined" sx={{ p: 2 }}>
          <Box component="dl" sx={{ m: 0 }} aria-label="Node settings">
            <Row label="Display name">{node.displayName}</Row>
            <Row label="Public host">{node.settings.publicHost}</Row>
            <Row label="Serve pattern">{SERVE_WORD[node.settings.servePattern]}</Row>
            <Row label="Tailnet machine">{node.settings.tailnetMachine ?? 'Not set'}</Row>
            <Row label="Gateway">{node.settings.gatewayEnabled ? 'Enabled' : 'Disabled'} · priority {node.settings.gatewayPriority}</Row>
            <Row label="Catalog pin">{node.settings.catalogPin ?? 'Follows the approved catalog'}</Row>
          </Box>
        </Paper>
      )}

      {may.admin ? (
        <Paper variant="outlined" component="section" aria-label="Tailnet IP" sx={{ p: 2 }}>
          <Typography variant="h6" component="h2">
            Tailnet IP
          </Typography>
          <Typography data-testid="tailnet-ip" sx={{ my: 1, fontFamily: 'monospace' }}>
            {revealed !== null ? revealed : node.settings.tailnetIpSet ? MASKED_TAILNET_IP : 'Not set'}
          </Typography>
          {revealError ? <Alert severity="error" sx={{ mb: 1 }}>{revealError}</Alert> : null}
          <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: "wrap" }}>
            {node.settings.tailnetIpSet && revealed === null ? (
              <Button size="small" variant="outlined" onClick={() => void reveal()} disabled={revealing}>
                Reveal
              </Button>
            ) : null}
            {revealed !== null ? (
              <Button size="small" variant="outlined" onClick={() => setRevealed(null)}>
                Hide
              </Button>
            ) : null}
            <Button size="small" variant="outlined" onClick={() => { setIpEditing(true); setIpValue(''); }}>
              {node.settings.tailnetIpSet ? 'Change…' : 'Set…'}
            </Button>
            {node.settings.tailnetIpSet ? (
              <Button size="small" color="warning" onClick={() => void save({ tailnetIp: null }).then(() => setRevealed(null))} disabled={saving}>
                Clear
              </Button>
            ) : null}
          </Stack>
          {ipEditing ? (
            <Box component="form" sx={{ mt: 2, display: 'flex', gap: 1, alignItems: 'flex-start', flexWrap: 'wrap' }} onSubmit={(event) => { event.preventDefault(); void save({ tailnetIp: ipValue }).then((ok) => { if (ok) { setIpEditing(false); setRevealed(null); } }); }}>
              <TextField label="New tailnet IP" size="small" value={ipValue} onChange={(event) => setIpValue(event.target.value)} autoComplete="off" error={Boolean(problems.fields.tailnetIp)} helperText={problems.fields.tailnetIp} />
              <Button type="submit" variant="contained" disabled={saving || ipValue.trim() === ''}>
                Save IP
              </Button>
              <Button onClick={() => setIpEditing(false)}>Cancel</Button>
            </Box>
          ) : null}
        </Paper>
      ) : null}

      {may.admin ? (
        <Paper variant="outlined" component="section" aria-label="Retire node" sx={{ p: 2, borderColor: 'error.main' }}>
          <Typography variant="h6" component="h2" color="error">
            Retire node
          </Typography>
          <Typography variant="body2" sx={{ my: 1 }}>
            Retiring is permanent. The node's credential is revoked, its key is deleted, commands still waiting are cancelled and the id {node.nodeId} is never used again.
          </Typography>
          <Button color="error" variant="outlined" onClick={() => setRetireOpen(true)}>
            Retire node…
          </Button>
        </Paper>
      ) : null}

      <TypedConfirmDialog
        open={retireOpen}
        title={`Retire ${node.nodeId}?`}
        confirmLabel="Retire node"
        expected={node.nodeId}
        onClose={() => setRetireOpen(false)}
        onConfirm={async () => {
          try {
            await retireNode(node.nodeId, node.nodeId);
            setRetireOpen(false);
            navigate('/lab');
            return null;
          } catch (error) {
            return problemsOf(error).general ?? 'Could not retire the node.';
          }
        }}
      >
        <Typography>
          This cannot be undone. {node.nodeId} stops being listed, its credential stops working and the id is never reused. A new computer needs a new node.
        </Typography>
      </TypedConfirmDialog>
    </Box>
  );
}
