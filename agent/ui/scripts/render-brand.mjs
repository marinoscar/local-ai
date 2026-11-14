#!/usr/bin/env node
// Renders the README images offline (ADR-0012), with the bundled Atkinson Hyperlegible font:
//   packages/ui/brand/banner.html            -> packages/ui/brand/banner.png (1280x400)
//   --screens <baseURL>                      -> docs/images/app-*.png: Home, Help and Manage screens of a running
//                                               agent or of e2e/fake-agent.mjs (Corona theme)
// Usage (in agent/ui): node scripts/render-brand.mjs [--screens http://127.0.0.1:7071]
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { chromium } from "@playwright/test";

const here = dirname(fileURLToPath(import.meta.url));
const repo = join(here, "..", "..", "..");
const font = (w) =>
  readFileSync(join(here, "..", "node_modules", "@fontsource", "atkinson-hyperlegible", "files", `atkinson-hyperlegible-latin-${w}-normal.woff2`)).toString("base64");
const fontCSS = [400, 700]
  .map((w) => `@font-face{font-family:"Atkinson Hyperlegible";font-weight:${w};src:url(data:font/woff2;base64,${font(w)}) format("woff2")}`)
  .join("");

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 400 }, deviceScaleFactor: 2 });
  await page.goto(pathToFileURL(join(repo, "packages", "ui", "brand", "banner.html")).href);
  await page.addStyleTag({ content: fontCSS });
  await page.evaluate(() => document.fonts.ready);
  await page.screenshot({ path: join(repo, "packages", "ui", "brand", "banner.png") });
  console.log("wrote packages/ui/brand/banner.png");

  const i = process.argv.indexOf("--screens");
  if (i > 0) {
    const base = process.argv[i + 1];
    const ctx = await browser.newContext({ viewport: { width: 1366, height: 768 }, baseURL: base });
    await ctx.request.post("/__reset").catch(() => undefined);
    const shots = [
      ["app-home", "/portal", { mode: "online", session: "full" }],
      ["app-survival", "/portal", { mode: "survival", session: "full" }],
      ["app-mode", "/portal/manage/mode", { mode: "online", session: "full" }],
      ["app-help", "/portal/help", { mode: "online", session: "full" }],
    ];
    for (const [name, path, status] of shots) {
      await ctx.request.post("/__status", { data: status }).catch(() => undefined);
      const p = await ctx.newPage();
      await p.goto(path);
      await p.waitForTimeout(1200);
      await p.screenshot({ path: join(repo, "docs", "images", `${name}.png`) });
      await p.close();
      console.log(`wrote docs/images/${name}.png`);
    }
  }
} finally {
  await browser.close();
}
