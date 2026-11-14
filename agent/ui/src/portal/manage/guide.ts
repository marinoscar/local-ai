import type { Key } from "../i18n/i18n";

// The in-app guide (dashboard.md section 4.7): the order of its sections and lines. Every line is a
// key of i18n/manage/guide.*.json; no terminal command appears outside EXPERT (guide.test.ts).

export const EMERGENCY: Key[] = [
  "guide.emergency.1",
  "guide.emergency.2",
  "guide.emergency.3",
  "guide.emergency.4",
];

export const CAN: Key[] = [
  "guide.can.chat",
  "guide.can.photo",
  "guide.can.talk",
  "guide.can.library",
  "guide.can.maps",
  "guide.can.manuals",
  "guide.can.status",
  "guide.can.brain",
  "guide.can.firstaid",
  "guide.can.mode",
  "guide.can.content",
  "guide.can.models",
  "guide.can.health",
  "guide.can.system",
  "guide.can.pin",
];

export interface Task {
  id: string;
  title: Key;
  steps: Key[];
}

const task = (id: string, n: number): Task => ({
  id,
  title: `guide.task.${id}.title` as Key,
  steps: Array.from({ length: n }, (_, i) => `guide.task.${id}.${i + 1}` as Key),
});

export const TASKS: Task[] = [
  task("survivalOn", 2),
  task("survivalOff", 3),
  task("offline", 2),
  task("update", 3),
  task("import", 3),
  task("eject", 3),
  task("health", 2),
  task("appUpdate", 2),
  task("colors", 3),
];

/** The typed commands, for an expert only: [what it does, the command]. Commands are never translated. */
export const EXPERT: [Key, string][] = [
  ["guide.task.survivalOn.title", "aictl mode survival"],
  ["guide.task.survivalOff.title", "aictl mode online"],
  ["guide.task.offline.title", "aictl mode offline"],
  ["guide.task.update.title", "aictl ark sync"],
  ["guide.task.import.title", "aictl ark import"],
  ["guide.task.eject.title", "aictl ark eject <root-id>"],
  ["guide.task.health.title", "aictl doctor"],
  ["guide.task.appUpdate.title", "sudo aictl update"],
];
