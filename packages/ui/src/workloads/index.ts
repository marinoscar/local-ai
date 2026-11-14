// Workloads (E11, #512): "what this computer is doing" and one button per named workload. Shared by the agent UI, the
// cloud node page and the desktop shell. Plain React, no MUI and no inline style: style it through the `lai-*` classes;
// `@localai/ui/creative.css` is a neutral base.
export { WorkloadSwitcher, workloadLabel, CODE_WORKLOAD_FAILED, CODE_WORKLOAD_SURVIVAL, type WorkloadSwitcherProps } from './WorkloadSwitcher';
export { WORKLOAD_STRINGS, type WorkloadKey } from './strings';
export type { ActivateOptions, Workload, WorkloadProgress, WorkloadsResponse, WorkloadTransport } from './types';
export { ErrorNotice, type ErrorNoticeProps } from '../common/ErrorNotice';
export { TransportError, codeFix, codeMeaning, isDoctorCode, toErrorInfo, type TransportErrorInfo } from '../common/errors';
export { LangProvider, useLang, type Lang } from '../lang';
