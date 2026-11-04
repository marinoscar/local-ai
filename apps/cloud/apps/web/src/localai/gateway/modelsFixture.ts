import type { ModelRow } from './modelsTypes';

const policy = (patch: Partial<ModelRow['policy']> = {}): ModelRow['policy'] => ({
  enabled: true,
  aliases: [],
  onDemandLoad: false,
  loadWaitS: 120,
  firstByteTimeoutS: null,
  preferredNodes: [],
  stored: false,
  updatedAt: null,
  ...patch,
});

/** The wireframe of gateway.md section 3.8, plus a disabled model and one no node has. */
export function modelsFixture(): ModelRow[] {
  return [
    {
      modelId: 'qwen3-coder-next-q4km',
      displayName: 'Qwen3 Coder Next',
      roles: ['coder', 'chat'],
      policy: policy({ aliases: ['lab-coder'], stored: true, updatedAt: '2026-11-04T11:00:00.000Z' }),
      state: 'loaded',
      listed: true,
      nodes: [{ nodeId: 'ai-n1', status: 'loaded' }],
    },
    {
      modelId: 'qwen3.6-35b-a3b-q4km',
      displayName: 'Qwen 3.6 35B A3B',
      roles: ['chat'],
      policy: policy({ onDemandLoad: true, loadWaitS: 120, preferredNodes: ['ai-n2'], stored: true, updatedAt: '2026-11-04T11:05:00.000Z' }),
      state: 'loadable',
      listed: true,
      nodes: [{ nodeId: 'ai-n2', status: 'loadable' }],
    },
    {
      modelId: 'qwen3.5-4b-q4km',
      displayName: 'Qwen 3.5 4B',
      roles: ['chat'],
      policy: policy({ enabled: false, stored: true, updatedAt: '2026-11-04T11:10:00.000Z' }),
      state: 'unloaded',
      listed: false,
      nodes: [{ nodeId: 'ai-n2', status: 'loadable' }],
    },
    {
      modelId: 'whisper-small',
      displayName: 'Whisper Small',
      roles: ['stt'],
      policy: policy(),
      state: 'none',
      listed: false,
      nodes: [],
    },
  ];
}
