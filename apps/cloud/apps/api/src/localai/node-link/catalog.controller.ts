// `GET /api/nodes/{id}/localai/catalog` (docs/specs/cloud-app.md section 3.6): the approved catalog, or the node's
// `catalogPin`, with the `bundleSha256` as a strong ETag. Like every node route it carries `@NodeLinkAuth()`, so the
// handler only ever runs for the verified node.
//
// Declared by `CatalogModule` (which imports `NodeLinkModule` for the guard), not by `NodeLinkModule`, so the
// node-link module does not depend on the catalog slice.
import { Controller, Get, Headers, NotFoundException, Req, Res } from '@nestjs/common';
import { ApiOperation, ApiTags } from '@nestjs/swagger';
import type { LocalaiNode } from '@prisma/client';
import type { FastifyReply, FastifyRequest } from 'fastify';
import { buildResponseMeta } from '@marinoscar/platform-api/host';

import { LAI_CLD_CATALOG_UNAVAILABLE } from '../catalog/catalog.constants';
import { CatalogService, type NodeCatalogBody } from '../catalog/catalog.service';
import { NodeLinkAuth } from './node-link-auth.decorator';
import { CurrentLocalaiNode } from './node-link.types';

@ApiTags('localai-node-link')
@Controller('nodes/:id/localai')
@NodeLinkAuth()
export class NodeCatalogController {
  constructor(private readonly catalog: CatalogService) {}

  /**
   * Answers through the reply itself: a 304 must have NO body, and the platform's `{ data, meta }` interceptor would
   * wrap even an empty return value. The 200 body is therefore wrapped here with the platform's own `meta` builder,
   * so it is byte-for-byte the envelope every other route sends.
   */
  @Get('catalog')
  @ApiOperation({ summary: 'The catalog this node should run: its pin, else the approved one (strong ETag, If-None-Match -> 304)' })
  async catalogFor(
    @CurrentLocalaiNode() node: LocalaiNode,
    @Headers('if-none-match') ifNoneMatch: string | undefined,
    @Req() request: FastifyRequest,
    @Res() reply: FastifyReply,
  ): Promise<void> {
    const answer = await this.catalog.forNode(node, ifNoneMatch);
    if (answer.kind === 'missing') {
      // The agent falls back to GitHub or its local copy (catalog.md section 1) and reads `details.code`.
      throw new NotFoundException({
        message: answer.reason === 'catalog_pin_not_found' ? 'The catalog this node is pinned to does not exist.' : 'No catalog is approved yet.',
        details: { code: LAI_CLD_CATALOG_UNAVAILABLE, reason: answer.reason },
      });
    }
    // The ETag and a private no-cache policy go on 200 and 304 alike (RFC 9110 section 15.4.5).
    void reply.header('etag', answer.etag).header('cache-control', 'private, no-cache');
    if (answer.kind === 'not_modified') {
      await reply.status(304).send();
      return;
    }
    await reply.status(200).send({ data: answer.body satisfies NodeCatalogBody, meta: buildResponseMeta(request) });
  }
}
