// The 3-node lab of the wireframe, as the API returns it: the same file the API's builder test proves
// (apps/api/test/fixtures/localai/lab-3-nodes.json), so what these tests render is what the server computes.
import type { LabNodesResponse, NodeCardDto } from '@localai/ui';

import raw from '../../../../api/test/fixtures/localai/lab-3-nodes.json';

/** The instant the fixture was computed at (apps/api/test/localai/support/lab-3-nodes.inputs.ts: LAB_NOW). */
export const LAB_NOW = new Date('2026-11-03T12:00:00.000Z');

/** A fresh deep copy: a test may edit it. */
export const labFixture = (): LabNodesResponse => structuredClone(raw) as LabNodesResponse;

export const cardOf = (nodeId: string): NodeCardDto => {
  const card = labFixture().nodes.find((node) => node.nodeId === nodeId);
  if (!card) throw new Error(`no ${nodeId} in the fixture`);
  return card;
};
