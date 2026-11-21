import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises } from "@vue/test-utils";
import type { Router } from "vue-router";
import type { PluginCommand } from "@/services/plugin-service";

// executePluginCommandWithArgs runs prefilled arguments (search results,
// uriHandlers links, CodeLens) without a form or confirmation, so the command
// must be read-only both as declared and by the action it runs.
const host = vi.hoisted(() => ({ commands: [] as unknown[], invoke: vi.fn() }));
vi.mock("@/services/plugin-service", () => ({
  listPluginCommands: async () => host.commands,
  listPluginContexts: async () => ({}),
  invokePlugin: (...args: unknown[]) => host.invoke(...args),
}));
vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => true,
  listenAppEvent: () => () => undefined,
}));
vi.mock("@/services/command-registry", () => ({ registerCommand: () => () => undefined }));

const { executePluginCommandWithArgs, startPluginCommandSync } = await import("@/services/plugin-commands");

function command(id: string, overrides: Partial<PluginCommand> = {}): PluginCommand {
  return {
    pluginId: "acme",
    pluginName: "Acme",
    id,
    title: id,
    action: `${id}.run`,
    risk: "read",
    effectiveRisk: "read",
    ...overrides,
  } as PluginCommand;
}

describe("executePluginCommandWithArgs", () => {
  let stop: () => void = () => undefined;

  beforeEach(async () => {
    host.invoke.mockReset();
    host.invoke.mockResolvedValue(undefined);
    const missing = command("missing");
    delete (missing as Partial<PluginCommand>).effectiveRisk;
    host.commands = [
      command("read"),
      command("write-action", { effectiveRisk: "write" }),
      command("network-action", { effectiveRisk: "network" }),
      command("dangerous-action", { effectiveRisk: "dangerous" }),
      missing,
      command("declared-write", { risk: "write", effectiveRisk: "write" }),
    ];
    stop = startPluginCommandSync({} as Router);
    await flushPromises();
  });

  afterEach(() => stop());

  it("runs a command whose declared and effective risk are read", async () => {
    await executePluginCommandWithArgs("acme", "read", { args: { name: "x" } });
    expect(host.invoke).toHaveBeenCalledWith("acme", "read.run", JSON.stringify({ name: "x" }));
  });

  it.each(["write-action", "network-action", "dangerous-action", "missing", "declared-write"])(
    "refuses %s without invoking the plugin",
    async (id) => {
      await expect(executePluginCommandWithArgs("acme", id, { args: { name: "x" } })).rejects.toThrow(/not available/);
      await expect(executePluginCommandWithArgs("acme", id, { payload: "p" })).rejects.toThrow(/not available/);
      expect(host.invoke).not.toHaveBeenCalled();
    },
  );
});
