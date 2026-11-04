// The route handlers of the proxied endpoints: thin bindings from `GatewayHandlers` (routes.ts) to `GatewayService`.
import type { GatewayHandlers } from './routes';
import type { GatewayService } from './gateway.service';

export function proxyHandlers(service: Pick<GatewayService, 'handle'>): Pick<GatewayHandlers, 'chatCompletions' | 'completions' | 'embeddings'> {
  return {
    chatCompletions: (ctx) => service.handle('chat.completions', ctx),
    completions: (ctx) => service.handle('completions', ctx),
    embeddings: (ctx) => service.handle('embeddings', ctx),
  };
}
