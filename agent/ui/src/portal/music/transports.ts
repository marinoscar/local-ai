import { agentMusic, agentWorkloads } from "./agent";

let shared: {
  music: ReturnType<typeof agentMusic>;
  workloads: ReturnType<typeof agentWorkloads>;
} | null = null;

/** The agent transports of the music page and the workload switcher: one pair per page load. */
export function musicTransports() {
  shared ??= {
    music: agentMusic(),
    workloads: agentWorkloads(undefined, { musicFallback: true }),
  };
  return shared;
}
