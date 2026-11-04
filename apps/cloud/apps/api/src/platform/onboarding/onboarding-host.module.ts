// The onboarding slice's host ports, bound to this app. `PrismaService` comes
// from the global `PrismaModule`, `SystemSettingsService` from the global
// settings module.
import { Module } from '@nestjs/common';
import { ONBOARDING_DATA, ONBOARDING_FEATURES } from '@marinoscar/platform-api/onboarding';

import { OnboardingDataAdapter } from './onboarding-data.adapter';
import { OnboardingFeaturesAdapter } from './onboarding-features.adapter';

const PORTS = [
  { provide: ONBOARDING_DATA, useClass: OnboardingDataAdapter },
  { provide: ONBOARDING_FEATURES, useClass: OnboardingFeaturesAdapter },
];

@Module({ providers: PORTS, exports: PORTS.map((port) => port.provide) })
export class OnboardingHostModule {}
