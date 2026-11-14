import type { Strings } from '../common/strings';

const en = {
  title: 'What this computer is doing',
  loading: 'Checking what this computer is doing…',
  now: 'Now: {name}',
  nowNone: 'Now: no task is fully loaded',
  nextNone: 'Choose a task below. The computer loads what it needs.',
  empty: 'No tasks are set up on this computer yet.',
  switchTo: 'Switch to {name}',
  inUse: 'In use',
  switching: 'Switching to {name}… This can take a few minutes.',
  switchingStep: 'Step: {step}',
  done: '{name} is ready.',
  tooBig: 'Does not fit in this computer (code LAI-LLM-051).',
  missing: 'Not on this computer yet: {models}',
  force: 'Switch anyway',
  retry: 'Try again',
  listFailed: 'Could not read the tasks of this computer.',
};

const es: Record<keyof typeof en, string> = {
  title: 'Qué está haciendo esta computadora',
  loading: 'Revisando qué está haciendo esta computadora…',
  now: 'Ahora: {name}',
  nowNone: 'Ahora: ninguna tarea está cargada por completo',
  nextNone: 'Elija una tarea abajo. La computadora carga lo que necesita.',
  empty: 'Aún no hay tareas configuradas en esta computadora.',
  switchTo: 'Cambiar a {name}',
  inUse: 'En uso',
  switching: 'Cambiando a {name}… Puede tardar unos minutos.',
  switchingStep: 'Paso: {step}',
  done: '{name} está listo.',
  tooBig: 'No cabe en esta computadora (código LAI-LLM-051).',
  missing: 'Todavía no está en esta computadora: {models}',
  force: 'Cambiar de todos modos',
  retry: 'Intentar de nuevo',
  listFailed: 'No se pudieron leer las tareas de esta computadora.',
};

export type WorkloadKey = keyof typeof en;
export const WORKLOAD_STRINGS: Strings<WorkloadKey> = { en, es };
