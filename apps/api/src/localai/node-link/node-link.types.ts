// What the binding guard attaches to the request and how a handler reads it (docs/specs/cloud-app.md section 4.2,
// step 7). The guard is the ONLY code that loads the `localai_nodes` row for a node route; a handler receives the
// verified row through `@CurrentLocalaiNode()` and never looks a node up by the URL itself.
import { createParamDecorator, type ExecutionContext } from '@nestjs/common';
import type { LocalaiNode } from '@prisma/client';

declare module 'fastify' {
  interface FastifyRequest {
    /** Set by `LocalaiNodeBindingGuard` once the credential, the worker node and the Local AI node agree. */
    localaiNode?: LocalaiNode;
  }
}

/** The request as the guard sees it: Fastify's, plus what `JwtAuthGuard` records. */
export interface NodeLinkRequest {
  headers: { authorization?: string | string[] };
  params?: { id?: string };
  user?: { id?: string };
  authCredential?: { kind: 'jwt' } | { kind: 'pat'; tokenId: string } | { kind: 'node' };
  localaiNode?: LocalaiNode;
}

/** `@CurrentLocalaiNode() node: LocalaiNode`: the node the verified credential speaks for. */
export const CurrentLocalaiNode = createParamDecorator((_data: unknown, context: ExecutionContext): LocalaiNode => {
  const node = context.switchToHttp().getRequest<NodeLinkRequest>().localaiNode;
  // A route reached this decorator without the guard: refuse to continue rather than act on no node.
  if (!node) throw new Error('LocalaiNodeBindingGuard did not run for this route');
  return node;
});
