// Wizard step 2: the node credential (shown ONCE), the pinned install commands, and the live "Enrolled" / "Key received"
// indicators. The credential lives in the parent's state only; this component renders it masked and copies it from the
// prop. It is never put in the URL, in storage or in history.
import VisibilityIcon from '@mui/icons-material/Visibility';
import VisibilityOffIcon from '@mui/icons-material/VisibilityOff';
import { Alert, Box, Button, Checkbox, CircularProgress, FormControlLabel, Link, Stack, Typography } from '@mui/material';
import { useState } from 'react';

import { CodeBlock } from './CodeBlock';
import { CopyButton } from './CopyButton';
import type { InstallInfo, NodeStatus } from './wizardTypes';

export interface StepInstallProps {
  nodeId: string;
  /** The raw credential. Null once it has been cleared. */
  token: string | null;
  prefix: string;
  install: InstallInfo;
  status: NodeStatus | null;
  /** Clears the credential and moves on to the publish step. */
  onContinue: () => void;
}

function Indicator({ done, label, waiting }: { done: boolean; label: string; waiting: string }) {
  return (
    <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
      {done ? null : <CircularProgress size={16} aria-hidden />}
      <Typography color={done ? 'success.main' : 'text.primary'}>{done ? `✔ ${label}` : waiting}</Typography>
    </Stack>
  );
}

export function StepInstall({ nodeId, token, prefix, install, status, onContinue }: StepInstallProps) {
  const [shown, setShown] = useState(false);
  const [saved, setSaved] = useState(false);
  const enrolled = status?.phase === 'enrolled';
  const keyReceived = status?.keyReceived === true;
  const canContinue = saved || enrolled;

  return (
    <Stack spacing={3} sx={{ maxWidth: 760 }}>
      <Box component="section" aria-labelledby="credential-title">
        <Typography id="credential-title" variant="h6" component="h2" gutterBottom>
          Node credential (shown once)
        </Typography>
        {token ? (
          <>
            <Alert severity="warning" sx={{ mb: 1.5 }}>
              This is the only time the credential is shown. Copy it now and keep it private (a password manager) until the node is enrolled. It cannot be shown again; if you lose it, make a new one.
            </Alert>
            <Stack direction={{ xs: 'column', sm: 'row' }} spacing={1.5} sx={{ alignItems: { sm: 'center' } }}>
              <Box
                data-testid="credential"
                sx={{ flex: 1, minWidth: 0, p: 1.25, border: 1, borderColor: 'divider', borderRadius: 1, fontFamily: 'ui-monospace, Menlo, Consolas, monospace', wordBreak: 'break-all' }}
              >
                {shown ? token : `${prefix}${'•'.repeat(24)}`}
              </Box>
              <Stack direction="row" spacing={1}>
                <Button size="small" variant="outlined" onClick={() => setShown((value) => !value)} startIcon={shown ? <VisibilityOffIcon /> : <VisibilityIcon />}>
                  {shown ? 'Hide' : 'Show'}
                </Button>
                <CopyButton text={token} label="Copy" context="node credential" />
              </Stack>
            </Stack>
            <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
              Expires: never (you can revoke it). Bound to {nodeId}: it works for no other node.
            </Typography>
          </>
        ) : (
          <Alert severity="info">The credential is no longer on this page. It was shown once and cannot be shown again.</Alert>
        )}
      </Box>

      <Box component="section" aria-labelledby="install-title">
        <Typography id="install-title" variant="h6" component="h2" gutterBottom>
          On the new computer
        </Typography>
        {install.oneLiner ? (
          <Stack spacing={1.5}>
            <Typography>Open a terminal and run these three commands. The script is downloaded and checked first; it is never piped into a shell.</Typography>
            <CodeBlock label="Install commands">{install.oneLiner}</CodeBlock>
            <Stack direction="row" spacing={1.5} useFlexGap sx={{ alignItems: 'center', flexWrap: 'wrap' }}>
              <CopyButton text={install.oneLiner} label="Copy" context="install commands" />
            </Stack>
            <Typography variant="body2" component="div">
              <strong>Checksum</strong>{' '}
              {install.scriptSha256 ? (
                <Box component="code" sx={{ wordBreak: 'break-all' }} data-testid="script-sha256">
                  {install.scriptSha256}
                </Box>
              ) : (
                <span>
                  unknown, check the release page
                  {install.releasePageUrl ? (
                    <>
                      {' ('}
                      <Link href={install.releasePageUrl} target="_blank" rel="noopener noreferrer">
                        release page
                      </Link>
                      {')'}
                    </>
                  ) : null}
                </span>
              )}
              . {install.scriptSha256 ? 'The second command compares it for you and must print install-node.sh: OK. If it prints FAILED, stop and download the file again.' : 'The second command prints the sum of the file you downloaded; it must be the same.'}
            </Typography>
            <Typography>It asks for the credential above. Paste it and press Enter (nothing appears while you paste).</Typography>
          </Stack>
        ) : (
          <Alert severity="info">{install.note ?? 'No install command is available.'}</Alert>
        )}
      </Box>

      <Box component="section" aria-labelledby="waiting-title" aria-live="polite">
        <Typography id="waiting-title" variant="h6" component="h2" gutterBottom>
          Waiting for {nodeId}
        </Typography>
        <Stack spacing={1}>
          <Indicator done={enrolled} label="Enrolled" waiting={`Waiting for ${nodeId} to contact the cloud …`} />
          <Indicator done={keyReceived} label="Key received" waiting="Waiting for the node to send its key …" />
        </Stack>
      </Box>

      <Stack spacing={1}>
        {!enrolled && token ? (
          <FormControlLabel control={<Checkbox checked={saved} onChange={(event) => setSaved(event.target.checked)} />} label="I have copied the credential and stored it safely" />
        ) : null}
        <Box>
          <Button variant="contained" onClick={onContinue} disabled={!canContinue}>
            Continue to publish
          </Button>
        </Box>
        <Typography variant="body2" color="text.secondary">
          Continuing removes the credential from this page.
        </Typography>
      </Stack>
    </Stack>
  );
}
