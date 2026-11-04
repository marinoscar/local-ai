// The AI slice, configured once: core, the five providers, the catalogue,
// policy, keys, runtime, the `/api/ai` and `/api/admin/ai` routes and usage.
// Keys, models and limits are runtime configuration (the `ai` settings
// namespace and the credential stores), never an environment variable.
// A feature imports this `AiModule`, injects `AiService` and calls
// `AiService.forUser(userId, { orgId, feature })`; it never builds an SDK client.
import { AiModule as PlatformAiModule } from '@marinoscar/platform-api/ai';

import { AiHostModule } from './ai-host.module';

export const AiModule = PlatformAiModule.forRoot({ imports: [AiHostModule] });
