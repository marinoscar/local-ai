import { fixForCommandError } from './command-failure-fix';

describe('fixForCommandError', () => {
  it('uses the registry fix for a code the cloud stores itself, followed by the code', () => {
    expect(fixForCommandError({ code: 'LAI-CLD-030', message: 'The node did not pick this command up in time' })).toBe("Check that the cloud's background jobs run (Admin → Jobs) (LAI-CLD-030)");
    expect(fixForCommandError({ code: 'LAI-CLD-059', message: 'x' })).toBe('Nothing to do; the cloud shows the current state (LAI-CLD-059)');
  });

  it('uses the fix of the two agent codes a model load can fail with', () => {
    expect(fixForCommandError({ code: 'LAI-CAT-005', message: 'Model does not fit in free VRAM' })).toBe('Choose a smaller model. `aictl models --loadable` lists the ones that fit (LAI-CAT-005)');
    expect(fixForCommandError({ code: 'LAI-LLM-011', message: 'x' })).toContain('Unload a model first');
  });

  it("falls back to the agent's own message for any other code", () => {
    expect(fixForCommandError({ code: 'LAI-SVC-001', message: 'The library has no books' })).toBe('The library has no books (LAI-SVC-001)');
  });

  it('never returns an empty line', () => {
    expect(fixForCommandError(null)).not.toBe('');
    expect(fixForCommandError({})).not.toBe('');
    expect(fixForCommandError({ code: 'LAI-SVC-001', message: '   ' })).toContain('LAI-SVC-001');
  });
});
