// Types of scripts/gen-doctor-fixes.mjs for its web test.
export const AREAS: string[];
export interface RegistryRow {
  code: string;
  severity: string;
  meaning: string;
  fix: string;
}
export function parseRegistry(markdown: string): RegistryRow[];
export function render(rows: RegistryRow[]): string;
