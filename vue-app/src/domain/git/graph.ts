import type { GitLogEntry } from "@/services/git-service";

export interface GitGraphCommit {
  hash: string;
  row: number;
  lane: number;
  parents: string[];
  refs: string[];
}

export function assignGitGraphLanes(entries: readonly GitLogEntry[]): GitGraphCommit[] {
  const result: GitGraphCommit[] = [];
  const activeLanes: Array<string | null> = [];

  entries.forEach((entry, row) => {
    let lane = activeLanes.findIndex((hash) => hash === entry.hash);
    if (lane < 0) {
      lane = activeLanes.findIndex((hash) => hash === null);
      if (lane < 0) {
        lane = activeLanes.length;
        activeLanes.push(null);
      }
    }

    const parents = entry.parents ?? [];
    result.push({
      hash: entry.hash,
      row,
      lane,
      parents,
      refs: entry.refs ?? [],
    });
    activeLanes[lane] = parents[0] ?? null;

    for (const parent of parents.slice(1)) {
      if (activeLanes.includes(parent)) continue;
      const emptyLane = activeLanes.findIndex((hash) => hash === null);
      if (emptyLane >= 0) activeLanes[emptyLane] = parent;
      else activeLanes.push(parent);
    }

    while (activeLanes[activeLanes.length - 1] === null) activeLanes.pop();
  });

  return result;
}
