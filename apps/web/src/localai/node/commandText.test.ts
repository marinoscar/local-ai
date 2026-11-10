import { commandFixture } from './fixture';
import { describeCommand, elapsedText, failureText, resultSummary, statusText } from './commandText';

describe('describeCommand', () => {
  it('names every type in plain words', () => {
    expect(describeCommand('localai.model.load', { model_id: 'qwen3.5-9b-q4km' })).toBe('Load qwen3.5-9b-q4km');
    expect(describeCommand('model.unload', { model_id: 'abc-model' })).toBe('Unload abc-model');
    expect(describeCommand('ark.sync', { dry_run: true })).toBe('Preview the ark sync (dry run)');
    expect(describeCommand('ark.sync', { dry_run: false })).toBe('Sync the ark');
    expect(describeCommand('service.restart', { service: 'llama' })).toBe('Restart llama');
    expect(describeCommand('logs.tail', { unit: 'agent', lines: 50 })).toBe('Fetch the agent logs');
    expect(describeCommand('agent.update', { version: '0.4.2' })).toBe('Update the agent to 0.4.2');
    expect(describeCommand('localai.ensure.run', {})).toBe("Install what's missing");
    expect(describeCommand('mode.offline', {})).toBe('Go offline');
    expect(describeCommand('something.new', {})).toBe('something.new');
  });
});

describe('statusText', () => {
  it('queued waits for the node, running runs, settled says Done / Failed', () => {
    expect(statusText({ status: 'queued', nodeId: 'ai-n2' })).toBe('Waiting for ai-n2 (checks every few seconds)');
    expect(statusText({ status: 'running', nodeId: 'ai-n2' })).toBe('Running');
    expect(statusText({ status: 'succeeded', nodeId: 'ai-n2' })).toBe('Done');
    expect(statusText({ status: 'failed', nodeId: 'ai-n2' })).toBe('Failed');
    expect(statusText({ status: 'cancelled', nodeId: 'ai-n2' })).toBe('Cancelled');
    expect(statusText({ status: 'expired', nodeId: 'ai-n2' })).toBe('Not picked up in time');
  });
});

describe('elapsedText', () => {
  it('counts from the queue time, from the claim while running, and stops at the finish', () => {
    const now = new Date('2026-11-03T12:00:42.000Z');
    expect(elapsedText(commandFixture({ status: 'queued' }), now)).toBe('42 s');
    expect(elapsedText(commandFixture({ status: 'running', claimedAt: '2026-11-03T12:00:30.000Z' }), now)).toBe('12 s');
    expect(elapsedText(commandFixture({ status: 'succeeded', claimedAt: '2026-11-03T12:00:30.000Z', finishedAt: '2026-11-03T12:01:35.000Z' }), now)).toBe('1 min 5 s');
  });
});

describe('resultSummary', () => {
  it('summarises results without ever printing log text', () => {
    expect(resultSummary({ type: 'localai.model.load', result: { load_ms: 12_000, unloaded: ['other-model'] } })).toBe('loaded in 12 s, 1 other model unloaded');
    expect(resultSummary({ type: 'localai.model.download', result: { already_present: false, bytes_planned: 5_500_000_000 } })).toBe('5.1 GB planned');
    expect(resultSummary({ type: 'localai.model.download', result: { already_present: true, bytes_planned: 0 } })).toBe('already on disk');
    expect(resultSummary({ type: 'localai.ark.sync', result: { started: true, plan: { items_to_download: 3, bytes_to_download: 2_147_483_648, items_skipped_budget: 0 } } })).toBe('3 items to download (2 GB)');
    expect(resultSummary({ type: 'localai.key.rotate', result: { fingerprint: 'ab12…9f0e', cloud_synced: false } })).toContain('retry');
    expect(resultSummary({ type: 'localai.ensure.run', result: { started: true, state: 'installing', pending_steps: 3 } })).toBe('installing (3 parts left to do)');
    expect(resultSummary({ type: 'localai.ensure.run', result: { started: false, state: 'complete', pending_steps: 0 } })).toBe('nothing was missing');
    expect(resultSummary({ type: 'localai.ensure.run', result: { started: false, state: 'installing', pending_steps: 2 } })).toBe('an install was already running, or nothing could be started');
    expect(resultSummary({ type: 'localai.agent.update', result: { from_version: '0.4.1', to_version: '0.4.2', status: 'updated' } })).toBe('0.4.1 to 0.4.2');
    expect(resultSummary({ type: 'localai.logs.tail', result: { unit: 'agent', lines: ['a secret line'], truncated: false } })).toBeNull();
    expect(resultSummary({ type: 'localai.model.load', result: undefined })).toBeNull();
  });
});

describe('failureText', () => {
  it('gives the registry meaning and fix for a known code, and the agent message for an unknown one', () => {
    const known = failureText({ error: { code: 'LAI-LLM-011', message: 'ignored' } });
    expect(known).toMatchObject({ code: 'LAI-LLM-011', meaning: 'Not enough room to load this model without unloading others', fix: 'Unload a model first, or allow unloading others' });
    expect(failureText({ error: { code: 'LAI-CLD-030' } }).fix).toContain('background jobs');
    expect(failureText({ error: { message: 'disk is full' } })).toEqual({ code: null, meaning: null, fix: 'disk is full' });
    expect(failureText({ error: null }).fix).toContain('aictl doctor');
  });
});
