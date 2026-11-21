import { describe, expect, it, vi } from "vitest";
import type { AiResource } from "@/domain/ai/resource-registry";
import { terminalCapabilities, type TerminalAiDependencies } from "@/services/ai-tools/terminal-tools";

// Spec 5.6: the AI operating a pane of a background terminal tab switches to that tab first.
function setup() {
  const calls: string[] = [];
  const dependencies = {
    capabilities: {} as TerminalAiDependencies["capabilities"],
    registry: {} as TerminalAiDependencies["registry"],
    listResources: () => [],
    invoke: vi.fn(async (command: string) => {
      calls.push(`invoke:${command}`);
      return undefined as never;
    }),
    revealTerminalPane: vi.fn((id: string) => calls.push(`reveal:${id}`)),
  } satisfies TerminalAiDependencies;
  const capability = (name: string) => terminalCapabilities(dependencies).find((item) => item.tool.name === name)!;
  const resource = { id: "terminal:pane-b", metadata: { sessionId: "pane-b" } } as unknown as AiResource;
  return { calls, capability, resource };
}

describe("AI terminal tools reveal the target pane", () => {
  it("terminal.send_input reveals the pane before writing to it", async () => {
    const { calls, capability, resource } = setup();
    await capability("terminal.send_input").execute({ input: { input: "ls" }, resource } as never);
    expect(calls).toEqual(["reveal:pane-b", "invoke:write_to_shell"]);
  });

  it("terminal.resize reveals the pane before resizing it", async () => {
    const { calls, capability, resource } = setup();
    await capability("terminal.resize").execute({ input: { cols: 80, rows: 24 }, resource } as never);
    expect(calls).toEqual(["reveal:pane-b", "invoke:resize_local_shell"]);
  });

  it("the AI assistant reveals only panes of a background terminal tab", async () => {
    const { readFileSync } = await import("node:fs");
    const { resolve } = await import("node:path");
    const drawer = readFileSync(resolve(__dirname, "../../src/components/ai/AiAssistantDrawer.vue"), "utf8");
    expect(drawer).toMatch(/if \(isBackgroundTerminalPane\(tabsState, sessionId\)\) terminalWorkspace\.selectPane\(sessionId\);/);
    expect(drawer).toMatch(/revealTerminalPane: revealTerminalPaneForAi,/);
  });
});
