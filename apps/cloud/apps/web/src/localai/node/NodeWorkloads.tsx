// The workload row of the node page (docs/specs/cloud-app.md section 3.11, #514): "This computer is set for: Coding"
// and one button per workload of the node's last snapshot. A press (needs `localai_workloads:write`) queues
// `localai.workload.activate` through `POST /api/localai/nodes/:nodeId/workloads/:name/activate`; the command line
// above the tabs shows its progress, and a refusal or failure its doctor code and fix. Hidden behind the usual
// explanation while the node is in Offline or Survival mode.
import { usePermissions } from '@marinoscar/platform-web/identity/headless';

import type { NodeDetail } from '../api/types';
import { activateWorkload } from '../music/musicApi';
import { WorkloadSwitcher } from '../music/WorkloadSwitcher';
import { gateFor } from './commandPolicy';
import { useNodeCommands } from './useNodeCommands';

export const WORKLOADS_PERMISSION = 'localai_workloads:write';

export function NodeWorkloads({ node }: { node: NodeDetail }) {
  const commands = useNodeCommands();
  const { hasPermission } = usePermissions();
  const block = node.lastState?.workloads ?? null;
  const gate = gateFor({ ...node, acceptedTypes: null }, 'workload.activate');
  const busy = commands.activeFor('workload.activate');
  const busyName = busy && typeof busy.params.name === 'string' ? busy.params.name : null;
  return (
    <WorkloadSwitcher
      workloads={block ? block.items : null}
      active={block?.active ?? null}
      canSwitch={hasPermission(WORKLOADS_PERMISSION)}
      blockedReason={gate.allowed ? null : `${gate.reason} (${gate.code})`}
      busyName={busyName}
      onSwitch={(name) => void commands.run(() => activateWorkload(node.nodeId, name))}
    />
  );
}
