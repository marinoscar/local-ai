// The onboarding slice, configured once: `GET /api/onboarding` (the derived
// Get started and Setup guide checklists) and `GET /api/admin/onboarding/metrics`
// (aggregate activation metrics). The admin block and the metrics gate on the
// default `system_settings:read` (the Doctor's, whose report the admin steps
// reuse); the web Setup guide card declares the same string.
import { OnboardingModule } from '@marinoscar/platform-api/onboarding';

import { OnboardingHostModule } from './onboarding-host.module';

export const onboardingModule = OnboardingModule.forRoot({ imports: [OnboardingHostModule] });
