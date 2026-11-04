// `@NodeLinkAuth()`: the one decorator every `/api/nodes/{id}/localai/*` controller carries.
//
// It is EAB's `@Auth()` (which runs `JwtAuthGuard`, the roles and the permissions guards) followed by the binding
// guard. ORDER MATTERS: Nest runs a route's guards in the order they were registered, and `applyDecorators` registers
// them in the order given here, so the binding guard always runs after `JwtAuthGuard` has set `request.user` and
// `request.authCredential`. (Stacking `@Auth()` and `@UseGuards(...)` by hand depends on the order of the two lines
// and is easy to get backwards; `node-link-routes.spec.ts` fails if a Local AI node route lacks this order.)
//
// No permission is declared: a `nod_` credential has none of the org permissions (localai.permissions.ts); the
// binding guard is what authorizes a node route.
import { applyDecorators, UseGuards } from '@nestjs/common';
import { Auth } from '@marinoscar/platform-api/identity';

import { LocalaiNodeBindingGuard } from './binding.guard';

export function NodeLinkAuth(): ClassDecorator & MethodDecorator {
  return applyDecorators(Auth(), UseGuards(LocalaiNodeBindingGuard)) as ClassDecorator & MethodDecorator;
}
