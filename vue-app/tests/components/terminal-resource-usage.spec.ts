import { describe, expect, it } from "vitest";
import {
  formatTerminalMemory,
  terminalMemoryLevel,
  terminalTabUsage,
  type LocalShellUsage,
} from "@/domain/terminal/resource-usage";

const GiB = 1024 ** 3;
const MiB = 1024 ** 2;

function usage(sessionId: string, processes: [number, string, number][]): LocalShellUsage {
  return {
    sessionId,
    shellPid: processes[0]?.[0] ?? 0,
    bytes: processes.reduce((sum, [, , bytes]) => sum + bytes, 0),
    processCount: processes.length,
    processes: processes.map(([pid, name, bytes]) => ({ pid, name, bytes })),
  };
}

describe("terminal resource usage", () => {
  it("adds up the panes of a tab and lists their largest processes first", () => {
    const usages = new Map([
      ["p1", usage("p1", [[1, "zsh", 10 * MiB], [2, "claude", 400 * MiB]])],
      ["p2", usage("p2", [[3, "gpu.test", 1.5 * GiB]])],
      ["other", usage("other", [[9, "node", 3 * GiB]])],
    ]);
    const tab = terminalTabUsage(["p1", "p2", "ssh-pane"], usages)!;
    expect(tab.bytes).toBe(10 * MiB + 400 * MiB + 1.5 * GiB);
    expect(tab.processCount).toBe(3);
    expect(tab.processes.map((p) => [p.sessionId, p.name])).toEqual([
      ["p2", "gpu.test"],
      ["p1", "claude"],
      ["p1", "zsh"],
    ]);
    expect(tab.processes.map((p) => p.isShell)).toEqual([true, false, true]);
    expect(terminalTabUsage(["ssh-pane"], usages)).toBeUndefined();
  });

  it("keeps the largest processes of a tab", () => {
    const many = usage("p1", Array.from({ length: 12 }, (_, i) => [i + 1, `p${i}`, (i + 1) * MiB] as [number, string, number]));
    const tab = terminalTabUsage(["p1"], new Map([["p1", many]]))!;
    expect(tab.processes).toHaveLength(8);
    expect(tab.processes[0]!.name).toBe("p11");
    expect(tab.processCount).toBe(12);
  });

  it("shows memory from 1 GB and warns from 4 GB", () => {
    expect(terminalMemoryLevel(900 * MiB)).toBe("hidden");
    expect(terminalMemoryLevel(1 * GiB)).toBe("normal");
    expect(terminalMemoryLevel(4 * GiB)).toBe("high");
  });

  it("formats sizes compactly", () => {
    expect(formatTerminalMemory(512 * MiB)).toBe("512 MB");
    expect(formatTerminalMemory(1.25 * GiB)).toBe("1.3 GB");
    expect(formatTerminalMemory(26 * GiB)).toBe("26 GB");
    expect(formatTerminalMemory(300 * 1024)).toBe("0.3 MB");
  });
});
