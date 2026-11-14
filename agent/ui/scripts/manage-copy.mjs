#!/usr/bin/env node
// Prints the copy table of one Manage file pair for docs/specs/dashboard.md section 9, in the form
// i18n.test.ts checks: `| `key` | English | Español |`. Usage: node scripts/manage-copy.mjs <name>
// (common, mode, content, models, health, system, guide). Pipes are escaped.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const name = process.argv[2];
if (!name) {
  console.error("usage: node scripts/manage-copy.mjs <name>");
  process.exit(2);
}
const dir = join(dirname(fileURLToPath(import.meta.url)), "..", "src", "portal", "i18n", "manage");
const en = JSON.parse(readFileSync(join(dir, `${name}.en.json`), "utf8"));
const es = JSON.parse(readFileSync(join(dir, `${name}.es.json`), "utf8"));
const cell = (s) => s.replace(/\|/g, "\\|");
console.log("| Key | English | Español |");
console.log("|---|---|---|");
for (const k of Object.keys(en)) console.log(`| \`${k}\` | ${cell(en[k])} | ${cell(es[k] ?? "")} |`);
