// The handlers of `GET /v1/models` and `GET /v1/models/{id}` (docs/specs/gateway.md section 3.2, 3.4 and 4.4). They are
// plugged into the `GatewayHandlers` of `routes.ts`, so the pipeline has already run: gateway enabled, key authenticated.
// Recording a usage row for `endpoint: "models"` is the usage item's (GW-08), uniformly for every endpoint.
import { GatewayError } from './errors';
import type { GatewayHandler, GatewayHandlers } from './routes';
import type { ModelListing } from './models/listing';

/** `GET /v1/models`: what the key can request now, in the OpenAI list shape. */
export function listModelsHandler(listing: Pick<ModelListing, 'list'>): GatewayHandler {
  return async ({ principal, reply }) => reply.code(200).send(await listing.list(principal.key));
}

/** `GET /v1/models/{id}`: the entry, or 404 `model_not_found` (LAI-GW-020) when it is not listed for this key. */
export function getModelHandler(listing: Pick<ModelListing, 'find'>): GatewayHandler {
  return async ({ request, principal, reply, log }) => {
    const id = (request.params as { id?: string }).id ?? '';
    log.requested_model = id.slice(0, 200);
    const entry = await listing.find(principal.key, id);
    if (!entry) throw new GatewayError('LAI-GW-020');
    return reply.code(200).send(entry);
  };
}

/** The two handlers `registerGatewayRoutes` wires in. */
export function modelsHandlers(listing: Pick<ModelListing, 'list' | 'find'>): Pick<GatewayHandlers, 'listModels' | 'getModel'> {
  return { listModels: listModelsHandler(listing), getModel: getModelHandler(listing) };
}
