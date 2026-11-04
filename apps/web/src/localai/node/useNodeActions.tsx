// What the current user may do on this node, and the one button every tab uses for a command (issue #86). A button the
// user may not use is NOT rendered (a viewer sees no action buttons at all); a button the node cannot take right now is
// rendered disabled with the reason (the doctor-code texts of spec 3.9) in its title and in the page banner.
import { Button, type ButtonProps } from '@mui/material';
import { usePermissions } from '@marinoscar/platform-web/identity/headless';
import type { ReactNode } from 'react';

import type { NodeDetail } from '../api/types';
import { gateFor, requiredPermission } from './commandPolicy';
import { useNodeCommands } from './useNodeCommands';

export function useMay() {
  const { hasPermission } = usePermissions();
  return {
    /** The exact permission the API enforces for this command type. */
    send: (type: string) => hasPermission(requiredPermission(type)),
    admin: hasPermission('lab:admin'),
    operate: hasPermission('lab:operate'),
  };
}

export interface CommandButtonProps extends Omit<ButtonProps, 'onClick' | 'type'> {
  node: Pick<NodeDetail, 'nodeId' | 'phase' | 'mode' | 'acceptedTypes'>;
  /** The command type this button queues (decides who sees it, and the gate). */
  type: string;
  /** The model id, service or unit: a second click while the same command is in flight is disabled. */
  subject?: string;
  /** Called on click; return false to say you opened a dialog instead of sending. */
  onPress: () => void;
  /** An extra reason this button is disabled (a model that does not fit, ...). */
  disabledReason?: string | null;
  children: ReactNode;
}

export function CommandButton({ node, type, subject, onPress, disabledReason = null, children, ...rest }: CommandButtonProps) {
  const may = useMay();
  const commands = useNodeCommands();
  if (!may.send(type)) return null;
  const gate = gateFor(node, type);
  const inFlight = commands.activeFor(type, subject) !== undefined;
  const reason = gate.reason ?? disabledReason ?? (inFlight ? 'This command is already waiting or running' : null);
  const disabled = !gate.allowed || disabledReason !== null || inFlight;
  return (
    <span title={disabled && reason ? reason : undefined} style={{ display: 'inline-flex' }}>
      <Button size="small" variant="outlined" {...rest} disabled={disabled} onClick={onPress}>
        {children}
      </Button>
    </span>
  );
}
