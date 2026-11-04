// Models tab (docs/specs/cloud-app.md section 3.11): one row per model of the node's last snapshot with its state,
// whether the files are on disk and verified, whether it fits, and the actions the user may take. Load/Unload/Set
// default/Download are lab:operate, Delete files is lab:admin with the model id typed. Load is offered only for a model
// that is present, verified and loadable; otherwise the row shows the fit reason or the Download button. Everything
// goes through the command API; the page never talks to the node.
import CheckIcon from '@mui/icons-material/Check';
import CloseIcon from '@mui/icons-material/Close';
import MoreVertIcon from '@mui/icons-material/MoreVert';
import StarIcon from '@mui/icons-material/Star';
import { Alert, Box, Chip, IconButton, Menu, MenuItem, Paper, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, Typography } from '@mui/material';
import { useState } from 'react';

import type { NodeDetail, SnapshotModel } from '../../api/types';
import { TypedConfirmDialog, ConfirmDialog } from '../ConfirmDialog';
import { gateFor } from '../commandPolicy';
import { formatBytes } from '../format';
import { CommandButton, useMay } from '../useNodeActions';
import { useNodeCommands } from '../useNodeCommands';

type Dialog = { kind: 'download'; model: SnapshotModel } | { kind: 'delete'; model: SnapshotModel } | null;

function StateChip({ status }: { status: SnapshotModel['status'] }) {
  switch (status) {
    case 'loaded':
      return <Chip size="small" color="success" label="loaded" />;
    case 'loading':
      return <Chip size="small" color="warning" variant="outlined" label="loading" />;
    case 'failed':
      return <Chip size="small" color="error" label="failed" />;
    default:
      return <span aria-label="not loaded">—</span>;
  }
}

function OnDisk({ model }: { model: SnapshotModel }) {
  if (model.present && model.verified) return <Box component="span" sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.5 }}><CheckIcon color="success" fontSize="inherit" />verified</Box>;
  if (model.present) return <Box component="span" sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.5 }}><CheckIcon color="warning" fontSize="inherit" />not verified</Box>;
  return <Box component="span" sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.5 }}><CloseIcon color="error" fontSize="inherit" />not on disk</Box>;
}

function Fits({ model }: { model: SnapshotModel }) {
  if (model.loadable) return <Box component="span" sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.5 }}><CheckIcon color="success" fontSize="inherit" />fits</Box>;
  return <Box component="span" sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.5 }}><CloseIcon color="error" fontSize="inherit" />{model.loadable_reason ?? "doesn't fit"}</Box>;
}

export function ModelsTab({ node }: { node: NodeDetail }) {
  const commands = useNodeCommands();
  const may = useMay();
  const [dialog, setDialog] = useState<Dialog>(null);
  const [menu, setMenu] = useState<{ anchor: HTMLElement; model: SnapshotModel } | null>(null);
  const models = node.lastState?.models ?? [];

  const run = (type: string, params: Record<string, unknown>) => void commands.send(type, params);

  if (!node.lastState) {
    return <Alert severity="info">{node.nodeId} has not reported its models yet.</Alert>;
  }
  if (models.length === 0) {
    return <Alert severity="info">{node.nodeId} reports no models.</Alert>;
  }

  const sizeOf = (id: string) => node.modelSizes[id];

  return (
    <>
      <TableContainer component={Paper} variant="outlined" tabIndex={0} role="region" aria-label="Models">
        <Table size="small" aria-label="Models of this node">
          <TableHead>
            <TableRow>
              <TableCell>Model</TableCell>
              <TableCell>State</TableCell>
              <TableCell>On disk</TableCell>
              <TableCell>Fits?</TableCell>
              <TableCell>Actions</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {models.map((model) => {
              const ready = model.present && model.verified;
              const size = sizeOf(model.id);
              return (
                <TableRow key={model.id} data-testid={`model-row-${model.id}`}>
                  <TableCell sx={{ overflowWrap: 'anywhere' }}>
                    {model.id}
                    {model.default ? <StarIcon color="warning" fontSize="inherit" titleAccess="default model" sx={{ ml: 0.5, verticalAlign: 'middle' }} /> : null}
                  </TableCell>
                  <TableCell><StateChip status={model.status} /></TableCell>
                  <TableCell><OnDisk model={model} /></TableCell>
                  <TableCell><Fits model={model} /></TableCell>
                  <TableCell>
                    <Box sx={{ display: 'flex', gap: 0.5, flexWrap: 'wrap', alignItems: 'center' }}>
                      {ready && model.loadable && model.status === 'loaded' ? (
                        <CommandButton node={node} type="model.unload" subject={model.id} onPress={() => run('model.unload', { model_id: model.id })}>
                          Unload
                        </CommandButton>
                      ) : null}
                      {ready && model.loadable && model.status !== 'loaded' && model.status !== 'loading' ? (
                        <CommandButton node={node} type="model.load" subject={model.id} onPress={() => run('model.load', { model_id: model.id })}>
                          Load
                        </CommandButton>
                      ) : null}
                      {ready && model.loadable && model.default ? (
                        <Chip size="small" variant="outlined" icon={<CheckIcon />} label="Default" />
                      ) : null}
                      {ready && model.loadable && !model.default ? (
                        <CommandButton node={node} type="model.set_default" subject={model.id} onPress={() => run('model.set_default', { model_id: model.id })}>
                          Set default
                        </CommandButton>
                      ) : null}
                      {!ready && model.loadable ? (
                        <CommandButton node={node} type="model.download" subject={model.id} onPress={() => setDialog({ kind: 'download', model })}>
                          {size ? `Download ${formatBytes(size)}` : 'Download'}
                        </CommandButton>
                      ) : null}
                      {!ready && !model.loadable ? (
                        <Typography variant="body2" color="text.secondary">
                          ({model.loadable_reason ?? "doesn't fit this node"})
                        </Typography>
                      ) : null}
                      {ready && !model.loadable ? (
                        <Typography variant="body2" color="text.secondary">
                          ({model.loadable_reason ?? "doesn't fit this node"})
                        </Typography>
                      ) : null}
                      {may.send('model.delete') && model.present ? (
                        <IconButton size="small" aria-label={`More actions for ${model.id}`} aria-haspopup="menu" onClick={(event) => setMenu({ anchor: event.currentTarget, model })}>
                          <MoreVertIcon fontSize="small" />
                        </IconButton>
                      ) : null}
                    </Box>
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </TableContainer>

      <Menu anchorEl={menu?.anchor ?? null} open={menu !== null} onClose={() => setMenu(null)}>
        <MenuItem
          disabled={!gateFor(node, 'model.delete').allowed || (menu?.model.roots ?? []).length === 0}
          onClick={() => {
            if (menu) setDialog({ kind: 'delete', model: menu.model });
            setMenu(null);
          }}
        >
          Delete files…
        </MenuItem>
      </Menu>

      <ConfirmDialog
        open={dialog?.kind === 'download'}
        title={`Download ${dialog?.model.id ?? ''}?`}
        confirmLabel="Download"
        onClose={() => setDialog(null)}
        onConfirm={async () => {
          const outcome = await commands.send('model.download', { model_id: dialog!.model.id }, { silent: true });
          if (outcome.ok) setDialog(null);
          return outcome.ok ? null : outcome.message;
        }}
      >
        <Typography>
          {dialog?.kind === 'download' && sizeOf(dialog.model.id)
            ? `Planned size: ${formatBytes(sizeOf(dialog.model.id)!)}.`
            : `${node.nodeId} reports the planned size when it starts.`}{' '}
          It downloads over the node's internet connection and is checked before it can be loaded.
        </Typography>
      </ConfirmDialog>

      <TypedConfirmDialog
        open={dialog?.kind === 'delete'}
        title={`Delete the files of ${dialog?.model.id ?? ''}?`}
        confirmLabel="Delete files"
        expected={dialog?.model.id ?? ''}
        onClose={() => setDialog(null)}
        onConfirm={async () => {
          const id = dialog!.model.id;
          const outcome = await commands.send('model.delete', { model_id: id, roots: dialog!.model.roots ?? [], confirm: id }, { silent: true });
          if (outcome.ok) setDialog(null);
          return outcome.ok ? null : outcome.message;
        }}
      >
        <Typography>
          This removes the model's files from: <strong>{(dialog?.model.roots ?? []).join(', ') || 'no location'}</strong>. The node always keeps the last verified copy and never deletes a loaded model.
        </Typography>
      </TypedConfirmDialog>
    </>
  );
}
