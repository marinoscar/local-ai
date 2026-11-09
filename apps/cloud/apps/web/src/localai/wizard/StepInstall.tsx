// Wizard step 2: the node credential (shown ONCE), the pinned install commands, and the live "Enrolled" / "Key received"
// indicators. The credential lives in the parent's state only; this component renders it masked and copies it from the
// prop. It is never put in the URL, in storage or in history.
import VisibilityIcon from '@mui/icons-material/Visibility';
import VisibilityOffIcon from '@mui/icons-material/VisibilityOff';
import { Alert, Box, Button, Checkbox, CircularProgress, FormControlLabel, Link, Stack, Typography } from '@mui/material';
import { useState } from 'react';

import { useT } from '../../i18n/LanguageProvider';
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
  const t = useT();
  const [shown, setShown] = useState(false);
  const [saved, setSaved] = useState(false);
  const enrolled = status?.phase === 'enrolled';
  const keyReceived = status?.keyReceived === true;
  const canContinue = saved || enrolled;

  return (
    <Stack spacing={3} sx={{ maxWidth: 760 }}>
      <Box component="section" aria-labelledby="credential-title">
        <Typography id="credential-title" variant="h6" component="h2" gutterBottom>
          {t('Node credential (shown once)', 'Credencial del nodo (se muestra una sola vez)')}
        </Typography>
        {token ? (
          <>
            <Alert severity="warning" sx={{ mb: 1.5 }}>
              {t('This is the only time the credential is shown. Copy it now and keep it private (a password manager) until the node is enrolled. It cannot be shown again; if you lose it, make a new one.', 'Esta es la única vez que se muestra la credencial. Cópiela ahora y guárdela en privado (un administrador de contraseñas) hasta que el nodo esté inscrito. No se puede mostrar de nuevo; si la pierde, haga una nueva.')}
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
                  {shown ? t('Hide', 'Ocultar') : t('Show', 'Mostrar')}
                </Button>
                <CopyButton text={token} label={t('Copy', 'Copiar')} context={t('node credential', 'credencial del nodo')} />
              </Stack>
            </Stack>
            <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
              {t(`Expires: never (you can revoke it). Bound to ${nodeId}: it works for no other node.`, `Vence: nunca (se puede revocar). Atada a ${nodeId}: no sirve para ningún otro nodo.`)}
            </Typography>
          </>
        ) : (
          <Alert severity="info">{t('The credential is no longer on this page. It was shown once and cannot be shown again.', 'La credencial ya no está en esta página. Se mostró una sola vez y no se puede mostrar de nuevo.')}</Alert>
        )}
      </Box>

      <Box component="section" aria-labelledby="install-title">
        <Typography id="install-title" variant="h6" component="h2" gutterBottom>
          {t('On the new computer', 'En la computadora nueva')}
        </Typography>
        {install.oneLiner ? (
          <Stack spacing={1.5}>
            <Typography>{t('Open a terminal and run these three commands. The script is downloaded and checked first; it is never piped into a shell.', 'Abra una terminal y ejecute estos tres comandos. El script se descarga y se comprueba primero; nunca se pasa directo a un shell.')}</Typography>
            <CodeBlock label={t('Install commands', 'Comandos de instalación')}>{install.oneLiner}</CodeBlock>
            <Stack direction="row" spacing={1.5} useFlexGap sx={{ alignItems: 'center', flexWrap: 'wrap' }}>
              <CopyButton text={install.oneLiner} label={t('Copy', 'Copiar')} context={t('install commands', 'comandos de instalación')} />
            </Stack>
            <Typography variant="body2" component="div">
              <strong>{t('Checksum', 'Suma de verificación')}</strong>{' '}
              {install.scriptSha256 ? (
                <Box component="code" sx={{ wordBreak: 'break-all' }} data-testid="script-sha256">
                  {install.scriptSha256}
                </Box>
              ) : (
                <span>
                  {t('unknown, check the release page', 'desconocida, revise la página de la versión')}
                  {install.releasePageUrl ? (
                    <>
                      {' ('}
                      <Link href={install.releasePageUrl} target="_blank" rel="noopener noreferrer">
                        {t('release page', 'página de la versión')}
                      </Link>
                      {')'}
                    </>
                  ) : null}
                </span>
              )}
              . {install.scriptSha256 ? t('The second command compares it for you and must print install-node.sh: OK. If it prints FAILED, stop and download the file again.', 'El segundo comando la compara por usted y debe mostrar install-node.sh: OK. Si muestra FAILED, deténgase y descargue el archivo de nuevo.') : t('The second command prints the sum of the file you downloaded; it must be the same.', 'El segundo comando muestra la suma del archivo que descargó; debe ser la misma.')}
            </Typography>
            <Typography>{t('It asks for the credential above. Paste it and press Enter (nothing appears while you paste).', 'Pide la credencial de arriba. Péguela y presione Enter (no aparece nada mientras pega).')}</Typography>
          </Stack>
        ) : (
          <Alert severity="info">{install.note ?? t('No install command is available.', 'No hay un comando de instalación disponible.')}</Alert>
        )}
      </Box>

      <Box component="section" aria-labelledby="waiting-title" aria-live="polite">
        <Typography id="waiting-title" variant="h6" component="h2" gutterBottom>
          {t('Waiting for', 'Esperando a')} {nodeId}
        </Typography>
        <Stack spacing={1}>
          <Indicator done={enrolled} label={t('Enrolled', 'Inscrito')} waiting={t(`Waiting for ${nodeId} to contact the cloud …`, `Esperando que ${nodeId} contacte a la nube …`)} />
          <Indicator done={keyReceived} label={t('Key received', 'Clave recibida')} waiting={t('Waiting for the node to send its key …', 'Esperando que el nodo envíe su clave …')} />
        </Stack>
      </Box>

      <Stack spacing={1}>
        {!enrolled && token ? (
          <FormControlLabel control={<Checkbox checked={saved} onChange={(event) => setSaved(event.target.checked)} />} label={t('I have copied the credential and stored it safely', 'Copié la credencial y la guardé en un lugar seguro')} />
        ) : null}
        <Box>
          <Button variant="contained" onClick={onContinue} disabled={!canContinue}>
            {t('Continue to publish', 'Continuar a publicar')}
          </Button>
        </Box>
        <Typography variant="body2" color="text.secondary">
          {t('Continuing removes the credential from this page.', 'Al continuar, la credencial se quita de esta página.')}
        </Typography>
      </Stack>
    </Stack>
  );
}
