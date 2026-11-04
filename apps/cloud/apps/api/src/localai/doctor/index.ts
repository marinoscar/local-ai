// The cloud's Doctor checks: import `LocalaiDoctorModule` (it registers every check with the platform's registry).
export { CLOUD_CODES, type CloudCode, type CloudCodeKey, type CloudCodeSeverity } from './codes';
export { LOCALAI_DOCTOR_CHECKS, LocalaiDoctorModule } from './doctor.module';
