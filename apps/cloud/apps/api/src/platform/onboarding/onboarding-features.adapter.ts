// =============================================================================
// The onboarding slice's ONBOARDING_FEATURES gate
// =============================================================================
//
// The same switches that hide the web's settings cards: `ai` (the AI kill
// switch) and `orgs` (multi-organization mode). A feature this app does not
// know is off, so a step gated on it is omitted. The AI switch is read from the
// AI slice's config service when that slice is mounted, and is off otherwise.
// =============================================================================

import { Injectable } from '@nestjs/common';
import { ModuleRef } from '@nestjs/core';
import { currentTenancyMode } from '@marinoscar/platform-api/identity';
import type { OnboardingFeatureGate } from '@marinoscar/platform-api/onboarding';

@Injectable()
export class OnboardingFeaturesAdapter implements OnboardingFeatureGate {
  constructor(private readonly modules: ModuleRef) {}

  async isEnabled(feature: string): Promise<boolean> {
    switch (feature) {
      case 'ai': {
        // Loaded lazily and by token: the AI slice may not be mounted.
        const { AiConfigService } = await import('@marinoscar/platform-api/ai');
        const config = this.modules.get(AiConfigService, { strict: false });
        return config ? config.isEnabled() : false;
      }
      case 'orgs':
        return currentTenancyMode() === 'multi';
      default:
        return false;
    }
  }
}
