// The storage slice's host port: the bypass client. The slice's cross-
// organization paths (the stale-upload sweep, the stranded-object count, the
// public avatar route, the avatar replacement across an org switch) reach it
// through `STORAGE_SYSTEM_DATA`, each naming its reason. The tenant client is
// core's `PLATFORM_PRISMA`, bound once by `PlatformHostModule`.
import { Global, Module } from '@nestjs/common';
import { STORAGE_SYSTEM_DATA } from '@marinoscar/platform-api/storage';

import { PrismaSystemService } from '../../prisma/prisma-system.service';

const BINDINGS = [{ provide: STORAGE_SYSTEM_DATA, useExisting: PrismaSystemService }];

@Global()
@Module({ providers: BINDINGS, exports: BINDINGS.map((binding) => binding.provide) })
export class StorageHostModule {}
