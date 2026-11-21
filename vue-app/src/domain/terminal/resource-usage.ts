/** Memory of the processes running in local terminals (local_shell_resource_usage). */

export interface TerminalProcessUsage {
  pid: number;
  name: string;
  bytes: number;
}

/** One local terminal session: its shell and everything started in it. */
export interface LocalShellUsage {
  sessionId: string;
  /** The session's shell: ended by closing the terminal, not on its own. */
  shellPid: number;
  bytes: number;
  processCount: number;
  /** Largest first, at most a few. */
  processes: TerminalProcessUsage[];
}

export interface TerminalTabProcessUsage extends TerminalProcessUsage {
  /** The pane (session) the process runs in. */
  sessionId: string;
  /** The pane's shell, which only closing the pane ends. */
  isShell: boolean;
}

/** The panes of a terminal tab together. */
export interface TerminalTabUsage {
  bytes: number;
  processCount: number;
  processes: TerminalTabProcessUsage[];
}

export const TERMINAL_TAB_TOP_PROCESSES = 8;
/** A tab shows its memory from here on: a shell or an idle AI agent stays below it. */
export const TERMINAL_MEMORY_SHOWN_BYTES = 1024 ** 3;
/** From here on the memory is shown as a warning. */
export const TERMINAL_MEMORY_HIGH_BYTES = 4 * 1024 ** 3;

export type TerminalMemoryLevel = "hidden" | "normal" | "high";

/** The usage of the panes paneIds (local sessions only), or undefined when none is measured. */
export function terminalTabUsage(paneIds: readonly string[], usages: ReadonlyMap<string, LocalShellUsage>): TerminalTabUsage | undefined {
  let measured = false;
  const tab: TerminalTabUsage = { bytes: 0, processCount: 0, processes: [] };
  for (const paneId of paneIds) {
    const usage = usages.get(paneId);
    if (!usage) continue;
    measured = true;
    tab.bytes += usage.bytes;
    tab.processCount += usage.processCount;
    for (const process of usage.processes) {
      tab.processes.push({ ...process, sessionId: paneId, isShell: process.pid === usage.shellPid });
    }
  }
  if (!measured) return undefined;
  tab.processes.sort((a, b) => b.bytes - a.bytes);
  tab.processes.length = Math.min(tab.processes.length, TERMINAL_TAB_TOP_PROCESSES);
  return tab;
}

export function terminalMemoryLevel(bytes: number): TerminalMemoryLevel {
  if (bytes >= TERMINAL_MEMORY_HIGH_BYTES) return "high";
  if (bytes >= TERMINAL_MEMORY_SHOWN_BYTES) return "normal";
  return "hidden";
}

/** "512 MB", "1.3 GB", "26 GB". */
export function formatTerminalMemory(bytes: number): string {
  const gib = bytes / 1024 ** 3;
  if (gib >= 10) return `${Math.round(gib)} GB`;
  if (gib >= 1) return `${gib.toFixed(1)} GB`;
  const mib = bytes / 1024 ** 2;
  return mib >= 1 ? `${Math.round(mib)} MB` : `${mib.toFixed(1)} MB`;
}
