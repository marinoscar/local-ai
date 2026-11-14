import { describe, expect, it } from "vitest";
import en from "../i18n/en.json";
import es from "../i18n/es.json";
import guideEn from "../i18n/manage/guide.en.json";
import guideEs from "../i18n/manage/guide.es.json";
import contentEn from "../i18n/manage/content.en.json";
import contentEs from "../i18n/manage/content.es.json";
import modeEn from "../i18n/manage/mode.en.json";
import modeEs from "../i18n/manage/mode.es.json";
import { CAN, EMERGENCY, EXPERT, TASKS } from "./guide";

// dashboard.md section 4.7: "Never a terminal command in steps 1 to 3; a command may appear only in a
// closed For an expert box." Checked on every text the guide and Help show outside that box.
const TERMINAL = /aictl|sudo|systemctl|`|127\.0\.0\.1:8888|\bterminal\b/i;

describe("the guide", () => {
  it("every line it shows exists in both languages", () => {
    const keys = [...EMERGENCY, ...CAN, ...TASKS.flatMap((t) => [t.title, ...t.steps])];
    for (const k of keys) {
      expect((guideEn as Record<string, string>)[k], k).toBeTruthy();
      expect((guideEs as Record<string, string>)[k], k).toBeTruthy();
    }
    for (const [k] of EXPERT) expect((guideEn as Record<string, string>)[k], k).toBeTruthy();
  });

  it("sends nobody to a terminal outside 'For an expert'", () => {
    for (const table of [guideEn, guideEs])
      for (const [k, v] of Object.entries(table)) {
        if (k.startsWith("guide.expert.")) continue;
        expect(v, k).not.toMatch(TERMINAL);
      }
    for (const table of [en, es])
      for (const [k, v] of Object.entries(table))
        if (k.startsWith("help.")) expect(v, k).not.toMatch(/aictl|sudo|127\.0\.0\.1:8888/);
  });

  it("names the buttons as the screens label them", () => {
    expect(guideEn["guide.emergency.2"]).toContain(`**${en["banner.startSurvival"]}**`);
    expect(guideEs["guide.emergency.2"]).toContain(`**${es["banner.startSurvival"]}**`);
    for (const b of ["btn.chat", "btn.library", "btn.maps"] as const) {
      expect(guideEn["guide.emergency.3"]).toContain(`**${en[b]}**`);
      expect(guideEs["guide.emergency.3"]).toContain(`**${es[b]}**`);
    }
  });

  it("names the Offline content and Mode buttons as those screens do", () => {
    for (const [g, c, m] of [
      [guideEn, contentEn, modeEn],
      [guideEs, contentEs, modeEs],
    ] as Record<string, string>[][]) {
      expect(g["guide.task.update.2"]).toContain(`**${c["content.sync"]}**`);
      expect(g["guide.task.import.2"]).toContain(`**${c["content.drives.title"]}**`);
      expect(g["guide.task.import.3"]).toContain(`**${c["content.copy"]}**`);
      expect(g["guide.task.eject.2"]).toContain(`**${c["content.eject"]}**`);
      expect(g["guide.task.survivalOff.3"]).toContain(`**${m["mode.leave.go"]}**`);
    }
  });

  it("the expert box has only typed commands", () => {
    for (const [, cmd] of EXPERT) expect(cmd).toMatch(/^(sudo )?aictl /);
  });
});
