import { ConflictException, UnprocessableEntityException } from '@nestjs/common';

import { COMMAND_TYPES, type CommandType } from '../contracts/commands';
import { assertModelOnNode, assertNodeTakesCommands, modelRequiredOnNode } from './command-refusals';

function thrown(fn: () => void): unknown {
  try {
    fn();
  } catch (error) {
    return error;
  }
  return undefined;
}

describe('assertNodeTakesCommands', () => {
  it('lets an enrolled node in Online mode, or with no snapshot yet, take commands', () => {
    expect(() => assertNodeTakesCommands({ phase: 'enrolled', mode: 'online' })).not.toThrow();
    expect(() => assertNodeTakesCommands({ phase: 'enrolled', mode: null })).not.toThrow();
  });

  it.each(['pending', 'retired'] as const)('refuses a %s node with 409 and LAI-CLD-035, whatever its mode', (phase) => {
    const error = thrown(() => assertNodeTakesCommands({ phase, mode: 'online' })) as ConflictException;
    expect(error).toBeInstanceOf(ConflictException);
    expect((error.getResponse() as { details: Record<string, unknown> }).details).toMatchObject({ code: 'LAI-CLD-035', phase });
    expect(error.message).not.toMatch(/token|secret/i);
  });

  it.each(['offline', 'survival'] as const)('refuses a node in %s mode with 422 and LAI-CLD-034 in plain words', (mode) => {
    const error = thrown(() => assertNodeTakesCommands({ phase: 'enrolled', mode })) as UnprocessableEntityException;
    expect(error).toBeInstanceOf(UnprocessableEntityException);
    expect((error.getResponse() as { details: Record<string, unknown> }).details).toMatchObject({ code: 'LAI-CLD-034', reason: 'node_offline_mode', mode });
    expect(error.message).toBe(`This computer is in ${mode === 'offline' ? 'Offline' : 'Survival'} mode. Only someone at it can switch it back.`);
  });

  it('the phase wins over the mode (a retired node last seen offline is "retired", not "offline")', () => {
    const error = thrown(() => assertNodeTakesCommands({ phase: 'retired', mode: 'offline' })) as ConflictException;
    expect(error).toBeInstanceOf(ConflictException);
  });
});

describe('modelRequiredOnNode', () => {
  it('names the model for every model command except the download that fetches it', () => {
    const params = { model_id: 'model-alpha' };
    const needing = COMMAND_TYPES.filter((type) => modelRequiredOnNode(type, params) !== null).sort();
    expect(needing).toEqual(['localai.model.delete', 'localai.model.load', 'localai.model.set_default', 'localai.model.unload']);
    expect(modelRequiredOnNode('localai.model.download', params)).toBeNull();
  });

  it('is null for a type with no model, even when the params happen to carry one', () => {
    for (const type of COMMAND_TYPES.filter((candidate: CommandType) => !candidate.startsWith('localai.model.'))) expect(modelRequiredOnNode(type, {})).toBeNull();
  });
});

describe('assertModelOnNode', () => {
  it('accepts a model the snapshot lists as present', () => {
    expect(() => assertModelOnNode({ present: true })).not.toThrow();
  });

  it('refuses a model the snapshot does not list, and one it lists but the node has not downloaded, with 422', () => {
    for (const row of [null, { present: false }]) {
      const error = thrown(() => assertModelOnNode(row)) as UnprocessableEntityException;
      expect(error).toBeInstanceOf(UnprocessableEntityException);
      expect(error.message).toContain('Download it first');
      expect((error.getResponse() as { details: Record<string, unknown> }).details).toEqual({ reason: 'model_not_on_node' });
    }
  });
});
