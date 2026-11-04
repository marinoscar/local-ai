import { estimateUsage, eventData, StreamUsageTracker } from './usage-from-events';

const ev = (payload: string, terminator = '\n\n'): Buffer => Buffer.from(`data: ${payload}${terminator}`, 'utf8');
const chat = (content: string | null): string => JSON.stringify({ choices: [{ index: 0, delta: content === null ? { role: 'assistant', content: null } : { content } }] });
const usageOnly = (prompt: number, completion: number, total = prompt + completion): string =>
  JSON.stringify({ choices: [], usage: { prompt_tokens: prompt, completion_tokens: completion, total_tokens: total } });

describe('StreamUsageTracker', () => {
  it('forwards ordinary events and counts the ones that carry text', () => {
    const tracker = new StreamUsageTracker(false);
    for (const payload of [chat(null), chat('Hel'), chat(''), chat('lo'), JSON.stringify({ choices: [{ delta: {}, finish_reason: 'stop' }] })]) {
      expect(tracker.inspect(ev(payload))).toEqual({ forward: true, done: false });
    }
    expect(tracker.contentBearingEvents).toBe(2);
  });

  it('counts `text` (completions) and `reasoning_content` (thinking models) as content', () => {
    const tracker = new StreamUsageTracker(false);
    tracker.inspect(ev(JSON.stringify({ choices: [{ index: 0, text: 'x' }] })));
    tracker.inspect(ev(JSON.stringify({ choices: [{ index: 0, text: '' }] })));
    tracker.inspect(ev(JSON.stringify({ choices: [{ delta: { reasoning_content: 'think' } }] })));
    tracker.inspect(ev(JSON.stringify({ choices: [{ delta: { reasoning_content: null, content: null } }] })));
    expect(tracker.contentBearingEvents).toBe(2);
  });

  it('is not fooled by key names typed inside a text value', () => {
    const tracker = new StreamUsageTracker(false);
    // The quotes of the typed text are escaped by JSON, so the key pattern cannot match inside the value.
    tracker.inspect(ev(JSON.stringify({ choices: [{ delta: { role: 'assistant', content: null }, note: '"content":"x" "usage":{}' }] })));
    expect(tracker.contentBearingEvents).toBe(0);
    expect(tracker.result().usageSource).toBe('estimated');
  });

  it('drops the usage-only chunk when the client did not ask for it, and records its usage', () => {
    const tracker = new StreamUsageTracker(false);
    expect(tracker.inspect(ev(usageOnly(11, 4)))).toEqual({ forward: false, done: false });
    expect(tracker.result()).toEqual({ promptTokens: 11, completionTokens: 4, usageSource: 'upstream' });
  });

  it('forwards the usage-only chunk when the client asked for it, and still records its usage', () => {
    const tracker = new StreamUsageTracker(true);
    expect(tracker.inspect(ev(usageOnly(11, 4)))).toEqual({ forward: true, done: false });
    expect(tracker.result()).toEqual({ promptTokens: 11, completionTokens: 4, usageSource: 'upstream' });
  });

  it('accepts a pretty-printed usage-only chunk and CRLF framing', () => {
    const tracker = new StreamUsageTracker(false);
    const pretty = '{"choices": [], "usage": {"prompt_tokens": 2, "completion_tokens": 3}}';
    expect(tracker.inspect(ev(pretty, '\r\n\r\n')).forward).toBe(false);
    expect(tracker.result()).toMatchObject({ promptTokens: 2, completionTokens: 3 });
  });

  it('ignores `usage: null` on every chunk (OpenAI style) and does not take it for a usage chunk', () => {
    const tracker = new StreamUsageTracker(false);
    expect(tracker.inspect(ev(JSON.stringify({ choices: [{ delta: { content: 'a' } }], usage: null })))).toEqual({ forward: true, done: false });
    expect(tracker.result()).toEqual({ promptTokens: null, completionTokens: 1, usageSource: 'estimated' });
  });

  it('records usage from a chunk that also has choices, but forwards it (it carries content or a finish)', () => {
    const tracker = new StreamUsageTracker(false);
    const payload = JSON.stringify({ choices: [{ index: 0, text: 'x', finish_reason: 'stop' }], usage: { prompt_tokens: 5, completion_tokens: 1 } });
    expect(tracker.inspect(ev(payload))).toEqual({ forward: true, done: false });
    expect(tracker.result()).toEqual({ promptTokens: 5, completionTokens: 1, usageSource: 'upstream' });
  });

  it('ignores total_tokens: the sum is prompt + completion', () => {
    const tracker = new StreamUsageTracker(false);
    tracker.inspect(ev(usageOnly(10, 5, 999)));
    expect(tracker.result()).toEqual({ promptTokens: 10, completionTokens: 5, usageSource: 'upstream' });
  });

  it('forwards a usage-looking event whose usage is unusable', () => {
    const tracker = new StreamUsageTracker(false);
    expect(tracker.inspect(ev('{"choices":[],"usage":{"prompt_tokens":"many"}}')).forward).toBe(true);
    expect(tracker.result().usageSource).toBe('estimated');
  });

  it('recognises [DONE], with or without the space and with CRLF', () => {
    for (const raw of ['data: [DONE]\n\n', 'data:[DONE]\n\n', 'data: [DONE]\r\n\r\n']) {
      const tracker = new StreamUsageTracker(false);
      expect(tracker.inspect(Buffer.from(raw))).toEqual({ forward: true, done: true });
      expect(tracker.finished).toBe(true);
    }
  });

  it('does not take a content event that mentions [DONE] for the end', () => {
    expect(new StreamUsageTracker(false).inspect(ev(chat('[DONE]'))).done).toBe(false);
  });
});

describe('usage estimate and event data', () => {
  it('estimates completion tokens from content events and leaves the prompt unknown', () => {
    expect(estimateUsage(7)).toEqual({ promptTokens: null, completionTokens: 7, usageSource: 'estimated' });
    expect(new StreamUsageTracker(false).result()).toEqual({ promptTokens: null, completionTokens: 0, usageSource: 'estimated' });
  });

  it('joins the data fields of an event as the SSE standard says', () => {
    expect(eventData('data: {"a":\ndata: 1}\n\n')).toBe('{"a":\n1}');
    expect(eventData(': note\nevent: x\ndata:y\n\n')).toBe('y');
  });
});
