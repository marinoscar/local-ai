// Types for the unit tests that import packaged-app.mjs (the script itself stays plain JavaScript).
export const DEFAULT_APP_DIR: string;
export const DEFAULT_COPY_DIR: string;
export const EXECUTABLE: string;
export const EXPECTED_FUSES: Readonly<Record<number, boolean>>;
export function verifyPackagedApp(appDir: string): Promise<{ ok: boolean; problems: string[]; checks: string[] }>;
export function makeTestCopy(fromDir: string, toDir: string): Promise<string>;
