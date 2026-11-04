// The picture rule once object storage exists: a user may point their profile
// at an avatar they uploaded. Replaces the starter's default adapter (which
// knows no uploads) for both the identity and the settings slices.
import { Injectable } from '@nestjs/common';
import type { IdentityProfileImages } from '@marinoscar/platform-api/identity';
import type { NormalizedProfileSettings, SettingsProfileImages } from '@marinoscar/platform-api/settings';
import { isAvatarObjectFor, normalizeProfileSettings, resolveProfileImageUrl } from '@marinoscar/platform-api/storage';

import { PrismaSystemService } from '../../prisma/prisma-system.service';

@Injectable()
export class StorageProfileImages implements IdentityProfileImages, SettingsProfileImages {
  constructor(private readonly system: PrismaSystemService) {}

  resolveImageUrl(user: { id: string; providerProfileImageUrl: string | null }, storedProfile: unknown): string | null {
    return resolveProfileImageUrl(user, storedProfile);
  }

  hasUploadedImage(storedProfile: unknown): boolean {
    return normalizeProfileSettings(storedProfile).imageObjectId !== null;
  }

  normalize(stored: unknown): NormalizedProfileSettings {
    return normalizeProfileSettings(stored);
  }

  /**
   * The user's own avatar row, which may belong to an organization other than
   * the active one (a picture outlives an org switch): one lookup by id through
   * the bypass client, re-checked against the owner.
   */
  async isUploadedAvatar(userId: string, objectId: string): Promise<boolean> {
    const object = await this.system.asSystem('admin-aggregate').storageObject.findUnique({
      where: { id: objectId },
      select: { uploadedById: true, storageKey: true, status: true, mimeType: true, metadata: true },
    });
    return isAvatarObjectFor(object, userId);
  }
}
