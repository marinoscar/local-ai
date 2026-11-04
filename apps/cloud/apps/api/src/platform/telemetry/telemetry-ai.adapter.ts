// `TELEMETRY_AI` when the AI slice is mounted: every model call goes through
// `AiService.forUser(userId).runTools` (the gate, the key policy, the usage
// row), so the AI platform rules hold: no provider SDK and no key material ever
// reaches the telemetry slice. `assertEnabled` is `AiConfigService.assertEnabled()`,
// the very call `AiEnabledGuard` makes, so "AI is off" answers the same 403
// `AI_DISABLED`. Loaded only while the `ai` slice is enabled.
import { Injectable } from '@nestjs/common';
import {
  AiConfigService,
  AiError,
  AiService,
  defineTool,
  type AiDefinedTool,
  type AiInputItem,
} from '@marinoscar/platform-api/ai';
import type {
  TelemetryAiError,
  TelemetryAiPort,
  TelemetryAiSession,
  TelemetryAiTool,
  TelemetryAiToolDefinition,
  TelemetryAiToolLoopResult,
} from '@marinoscar/platform-api/telemetry';
import type { z } from 'zod';

@Injectable()
export class TelemetryAiAdapter implements TelemetryAiPort {
  constructor(
    private readonly ai: AiService,
    private readonly aiConfig: AiConfigService,
  ) {}

  forUser(userId: string): TelemetryAiSession {
    const client = this.ai.forUser(userId);
    return {
      runTools: async (request, options) => {
        const result = await client.runTools(
          { ...request, input: request.input as AiInputItem[], tools: request.tools as unknown as AiDefinedTool[] },
          options,
        );
        return result as TelemetryAiToolLoopResult;
      },
    };
  }

  defineTool<P extends z.ZodType, R>(definition: TelemetryAiToolDefinition<P, R>): TelemetryAiTool {
    return defineTool(definition) as unknown as TelemetryAiTool;
  }

  isAiError(error: unknown): error is TelemetryAiError {
    return error instanceof AiError;
  }

  assertEnabled(): Promise<void> {
    return this.aiConfig.assertEnabled();
  }
}
