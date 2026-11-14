import commonEn from "./common.en.json";
import commonEs from "./common.es.json";
import modeEn from "./mode.en.json";
import modeEs from "./mode.es.json";
import contentEn from "./content.en.json";
import contentEs from "./content.es.json";
import modelsEn from "./models.en.json";
import modelsEs from "./models.es.json";
import healthEn from "./health.en.json";
import healthEs from "./health.es.json";
import systemEn from "./system.en.json";
import systemEs from "./system.es.json";

// The copy of the Manage screens and of Home's additions (dashboard.md section 9). One pair of files
// per screen keeps each screen's text next to its own spec table; the merge below is what t() reads.
// A key must be unique across every file (i18n.test.ts checks it).

export const MANAGE_EN = {
  ...commonEn,
  ...modeEn,
  ...contentEn,
  ...modelsEn,
  ...healthEn,
  ...systemEn,
};

export const MANAGE_ES: Record<keyof typeof MANAGE_EN, string> = {
  ...commonEs,
  ...modeEs,
  ...contentEs,
  ...modelsEs,
  ...healthEs,
  ...systemEs,
};

/** Every pair, for the tests: [name, en, es]. */
export const MANAGE_FILES: [string, Record<string, string>, Record<string, string>][] = [
  ["common", commonEn, commonEs],
  ["mode", modeEn, modeEs],
  ["content", contentEn, contentEs],
  ["models", modelsEn, modelsEs],
  ["health", healthEn, healthEs],
  ["system", systemEn, systemEs],
];
