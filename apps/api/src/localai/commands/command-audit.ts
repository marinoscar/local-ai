// Audit actions of the command plane (docs/specs/cloud-app.md section 3.5). Permanent strings: the audit page and
// anything that filters `audit_events` match on them. `targetType` is `localai_command`, `targetId` the command id.
export const AUDIT_COMMAND_REQUESTED = 'localai.command.requested';
export const AUDIT_COMMAND_CANCELLED = 'localai.command.cancelled';
export const AUDIT_COMMAND_SETTLED = 'localai.command.settled';
export const AUDIT_COMMAND_TARGET = 'localai_command';
