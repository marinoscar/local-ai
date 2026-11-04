// The five fixture-based AI conformance suites, run against THIS app: the AI
// kill switch, the RBAC matrix, secret egress, key policy and jobs server-only.
// They boot the whole `AppModule` over a mocked database with the platform's
// in-memory AI harness (see `./ai-conformance/ai-fixture.ts`), so they need no
// PostgreSQL and run in the ordinary `npm test` tier. Present only while the AI
// slice is enabled (`packages/shared/slices.json`).
import '@marinoscar/platform-api/ai/testing';

import { join } from 'node:path';

import { runPlatformConformance } from '@marinoscar/platform-api/testing';

import { isSliceEnabled } from '../src/platform/slices/manifest';
import { aiConformanceFixture } from './ai-conformance/ai-fixture';

if (isSliceEnabled('ai')) {
  runPlatformConformance({
    sourceRoots: [join(__dirname, '..', 'src')],
    suites: {
      aiKillSwitch: { fixture: aiConformanceFixture },
      aiRbacMatrix: { fixture: aiConformanceFixture },
      aiSecretEgress: { fixture: aiConformanceFixture },
      aiKeyPolicy: { fixture: aiConformanceFixture },
      aiJobsServerOnly: { fixture: aiConformanceFixture },
    },
  });
} else {
  it.skip('the AI slice is not enabled (packages/shared/slices.json)', () => undefined);
}
