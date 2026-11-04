// Import for the side effect: registers everything the enabled slices add to
// the platform's static registries, once. `platform.ts` imports it before the
// first `forRoot()`; the conformance spec and the seed reach it through there
// (or through `register.ts` directly).
import { registerSlices } from './register';

registerSlices();
