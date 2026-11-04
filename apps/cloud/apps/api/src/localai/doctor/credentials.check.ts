// `localai.credentials` (LAI-CLD-032, critical): can the cloud read the keys it stores? (docs/specs/cloud-app.md
// sections 3.8, 5 and 7.) A lost or changed `SECRETS_ENCRYPTION_KEY` makes every stored node key unreadable, and the
// gateway answers 503 for every node without anything else looking broken.
//
// What it does, READ-ONLY:
//  - a node key is stored: it reads ONE through `CredentialsService.getSecret` and checks only that the call
//    resolved. The plaintext is never assigned to a variable that outlives the call, logged or returned. This is the
//    real proof: it fails for a missing key AND for a wrong one.
//  - no node key is stored yet: it encrypts and decrypts a fixed sentinel IN MEMORY under the same purpose. That
//    proves the key is present and well formed (it cannot prove it is the RIGHT key: nothing is stored to compare with).
//
// DEVIATION from issue #91 (documented in cloud-app.md section 7, "As built"): the issue asks for a set, get, delete of
// a throwaway credential when no key is stored. A Doctor check may not write (the platform's `doctor-read-only`
// contract, and AGENTS.md of this app), so the round trip stays in memory and nothing is ever written to `credentials`.
import { Inject, Injectable, Logger, type OnModuleInit } from '@nestjs/common';
import { decryptSecret, encryptSecret } from '@marinoscar/platform-api/core';
import { CredentialsService } from '@marinoscar/platform-api/credentials';
import { DoctorCheckRegistry, type DoctorCheck, type DoctorCheckOutcome } from '@marinoscar/platform-api/doctor';

import { LOCALAI_NODE_LLAMA_KEY_PURPOSE } from '../../platform/credentials/credential-purposes';
import { CLOUD_CODES } from './codes';
import { buildOutcome, errorName } from './doctor-outcome';
import { LOCALAI_DOCTOR_CATEGORY, LOCALAI_DOCTOR_CIPHER } from './doctor.constants';

/** An in-memory encrypt then decrypt under the node key purpose. Throws when the cipher cannot work. */
export interface CipherProbe {
  roundTrip(purpose: string): void;
}

/** The real probe: core's cipher, which reads `SECRETS_ENCRYPTION_KEY`. The sentinel is not a secret. */
export class PlatformCipherProbe implements CipherProbe {
  roundTrip(purpose: string): void {
    const sentinel = 'doctor-probe';
    if (decryptSecret(encryptSecret(sentinel, purpose), purpose) !== sentinel) throw new Error('round trip mismatch');
  }
}

@Injectable()
export class CredentialsDoctorCheck implements DoctorCheck, OnModuleInit {
  readonly id = 'localai.credentials';
  readonly category = LOCALAI_DOCTOR_CATEGORY;
  readonly label = 'Cloud can read its stored keys';
  readonly timeoutMs = 5_000;
  private readonly logger = new Logger(CredentialsDoctorCheck.name);

  constructor(
    private readonly registry: DoctorCheckRegistry,
    private readonly credentials: CredentialsService,
    @Inject(LOCALAI_DOCTOR_CIPHER) private readonly cipher: CipherProbe,
  ) {}

  onModuleInit(): void {
    this.registry.register(this);
  }

  async run(): Promise<DoctorCheckOutcome> {
    const purpose = LOCALAI_NODE_LLAMA_KEY_PURPOSE.purpose;
    let storedName: string | undefined;
    try {
      storedName = (await this.credentials.list(purpose))[0]?.name;
    } catch (error) {
      return this.unreadable(error);
    }

    try {
      if (storedName !== undefined) {
        // Only that the call resolved is looked at; `null` means the row vanished between the list and the read.
        await this.credentials.getSecret(purpose, storedName);
        return buildOutcome({ status: 'pass', detail: 'A stored node key was read and decrypted', data: { probe: 'stored key' } });
      }
      this.cipher.roundTrip(purpose);
      return buildOutcome({
        status: 'pass',
        detail: 'No node key is stored yet; the encryption key is present and works',
        data: { probe: 'in-memory round trip' },
      });
    } catch (error) {
      return this.unreadable(error);
    }
  }

  private unreadable(error: unknown): DoctorCheckOutcome {
    // The error NAME only: a decryption error's message is not ours to trust (spec section 6).
    this.logger.warn(`the credential store could not be read: ${errorName(error)}`);
    return buildOutcome({
      status: 'fail',
      code: CLOUD_CODES.credentialsUnreadable,
      remedy: 'The "Ask all nodes to re-send keys" action is POST /api/localai/nodes/resend-keys (Lab administrators)',
      error: errorName(error),
    });
  }
}
