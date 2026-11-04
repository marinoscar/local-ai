// The id vocabularies the command schemas share with the state snapshot (docs/specs/cloud-app.md section 3.6, the
// `state.schema.ts` block): the same three regular expressions. They are defined here, next to the command
// registry, so the command plane does not depend on the snapshot schema landing first; when `state.schema.ts` exists
// it must export these same values (the spec text is the one source), and this file re-exports from it.
import { z } from 'zod';

/** A model id of the catalog: lower case, digits, dot and dash, 3 to 64 characters. */
export const ModelId = z.string().regex(/^[a-z0-9][a-z0-9.\-]{2,63}$/);
/** A content id of the ark catalog (same shape as a model id). */
export const ContentId = ModelId;
/** An ark root id (ark-sync.md section 2): the internal disk or one ark drive. */
export const RootId = z.string().regex(/^(internal|ark-[0-9a-f]{8})$/);
