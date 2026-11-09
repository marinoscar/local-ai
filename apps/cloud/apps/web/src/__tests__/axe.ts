// The one way a component test runs axe (#377).
//
// `color-contrast` is switched off for every run, for two reasons. jsdom has no layout and no painting, so the rule can
// never report a real violation: it can only answer "needs review", which no test acts on. And it is the most
// expensive rule: it measures every text node and probes a canvas (jsdom prints "Not implemented:
// HTMLCanvasElement.getContext" for each one), which on a loaded CI runner turned a 1 s axe pass into several seconds.
// Contrast is a design-token decision, checked in the browser (tests/visual), not here.
import axe, { type AxeResults, type ElementContext, type RunOptions } from 'axe-core';

export function runAxe(context: ElementContext, options: RunOptions = {}): Promise<AxeResults> {
  return axe.run(context, { ...options, rules: { ...options.rules, 'color-contrast': { enabled: false } } });
}
