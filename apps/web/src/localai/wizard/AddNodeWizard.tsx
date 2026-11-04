// The Add-node wizard (`/lab/nodes/new`, lab:admin; docs/specs/cloud-app.md sections 3.11 and 4.8): describe the node,
// install it (the credential is shown once), publish it (the VPS checklist).
//
// THE CREDENTIAL. `token` is React state in this component and nowhere else: not in the URL, router state, storage or a
// ref, and the create request asked the browser not to cache. It is cleared when the operator continues to the publish
// step, and it dies with the component if they leave. While it is on screen and the node has not enrolled, closing the
// tab asks for confirmation.
//
// RESUMING. `?node=ai-n4` opens the publish step of a node that already exists (the credential is gone by then).
import { Box, Button, Step, StepLabel, Stepper, Typography } from '@mui/material';
import { usePermissions } from '@marinoscar/platform-web/identity/headless';
import { useEffect, useState } from 'react';
import { Link as RouterLink, useSearchParams } from 'react-router-dom';
import { Alert } from '@mui/material';

import { StepDescribe } from './StepDescribe';
import { StepInstall } from './StepInstall';
import { StepPublish } from './StepPublish';
import { defaultWizardClient, type WizardClient } from './wizardApi';
import type { CreateNodeResult, NodeStatus } from './wizardTypes';
import { usePolled } from './usePolled';

export const STATUS_POLL_MS = 3000;
const STEP_LABELS = ['Describe the node', 'Install on the node', 'Publish it (VPS)'] as const;
const NODE_PARAM = /^ai-n[1-9][0-9]{0,3}$/;

export interface AddNodeWizardProps {
  /** Replaces the transport (tests). */
  client?: WizardClient;
}

/** The result without the credential: what step 3 and the "no longer shown" state keep. */
type Created = Omit<CreateNodeResult, 'credential'> & { prefix: string };

export function AddNodeWizard({ client = defaultWizardClient }: AddNodeWizardProps) {
  const { hasPermission } = usePermissions();
  const [params] = useSearchParams();
  const resumeId = params.get('node');
  const resumable = resumeId !== null && NODE_PARAM.test(resumeId) ? resumeId : null;

  const [step, setStep] = useState<0 | 1 | 2>(resumable ? 2 : 0);
  const [created, setCreated] = useState<Created | null>(null);
  const [token, setToken] = useState<string | null>(null);
  const nodeId = created?.node.nodeId ?? resumable;

  // Poll every 3 s while the install step waits for the node; stop once both flags are green.
  const [settled, setSettled] = useState(false);
  const waiting = step === 1 && nodeId !== null && !settled;
  const statusPoll = usePolled<NodeStatus>(() => client.nodeStatus(nodeId ?? ''), STATUS_POLL_MS, waiting);
  useEffect(() => {
    if (statusPoll.data?.phase === 'enrolled' && statusPoll.data.keyReceived) setSettled(true);
  }, [statusPoll.data]);

  // Closing the tab while the only copy of the credential is on screen and unused: ask first.
  const credentialAtRisk = token !== null && statusPoll.data?.phase !== 'enrolled';
  useEffect(() => {
    if (!credentialAtRisk) return undefined;
    const warn = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', warn);
    return () => window.removeEventListener('beforeunload', warn);
  }, [credentialAtRisk]);

  const onCreated = (result: CreateNodeResult) => {
    const { credential, ...rest } = result;
    setCreated({ ...rest, prefix: credential.prefix });
    setToken(credential.token);
    setStep(1);
  };

  const onContinue = () => {
    setToken(null); // the credential leaves the page
    setStep(2);
  };

  if (!hasPermission('lab:admin')) {
    return (
      <Alert severity="warning" data-testid="not-allowed">
        Only administrators can add a node.
      </Alert>
    );
  }

  return (
    <Box component="section" aria-labelledby="wizard-title" sx={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 1.5, justifyContent: 'space-between' }}>
        <Typography id="wizard-title" variant="h4" component="h1">
          Add node
        </Typography>
        <Button component={RouterLink} to="/lab" variant="text">
          {step === 2 ? 'Back to the lab' : 'Cancel'}
        </Button>
      </Box>

      <Stepper activeStep={step} alternativeLabel aria-label="Progress" sx={{ '& .MuiStepLabel-label': { fontSize: { xs: '0.75rem', sm: '0.875rem' } } }}>
        {STEP_LABELS.map((label, index) => (
          <Step key={label} completed={index < step}>
            <StepLabel>{label}</StepLabel>
          </Step>
        ))}
      </Stepper>
      <Typography variant="overline" color="text.secondary" aria-live="polite">
        Step {step + 1} of 3 &middot; {STEP_LABELS[step]}
      </Typography>

      {step === 0 ? <StepDescribe client={client} onCreated={onCreated} /> : null}
      {step === 1 && created ? (
        <StepInstall nodeId={created.node.nodeId} token={token} prefix={created.prefix} install={created.install} status={statusPoll.data} onContinue={onContinue} />
      ) : null}
      {step === 2 && nodeId ? <StepPublish client={client} nodeId={nodeId} /> : null}
    </Box>
  );
}
