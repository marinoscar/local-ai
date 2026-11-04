// The data layer's offline invariants (`runDbConformance`, no database): the installed platform migrations equal
// the package's, the app's migration directories do not reorder the platform history, and every raw-SQL index and
// row-level-security policy the app lists in `prisma/platform.lock` is created by a migration and dropped by none,
// while no unlisted partial or expression index exists (`app-schema-objects`). That is what keeps the three Local AI
// partial unique indexes (migration `add_localai`) honest without a database.
import { join } from 'node:path';
import { runDbConformance } from '@marinoscar/platform-db';

runDbConformance({ appRoot: join(__dirname, '..', '..') });
