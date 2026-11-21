import { afterEach, describe, expect, it, vi } from "vitest";

const bridge = vi.hoisted(() => ({
  native: true,
  invoke: vi.fn(),
}));

vi.mock("@/services/desktop-bridge", () => ({
  isDesktopRuntime: () => bridge.native,
  invokeBackend: bridge.invoke,
}));

import { launchPluginAcpAgent, prepareAcpAgent, trustAcpAgent } from "@/services/plugin-acp-agent-service";

const FINGERPRINT = "b".repeat(64);

function hostLaunch(overrides: Record<string, unknown> = {}) {
  return {
    pluginId: "acme.tools",
    pluginName: "Acme Tools",
    pluginVersion: "1.0.0",
    point: "acpAgents",
    entryKey: "helper",
    title: "Helper",
    argv: ["/plugins/acme.tools/bin/agent", "--acp"],
    env: null,
    cwd: "${projectRoot}",
    pluginExecutable: true,
    pluginFiles: null,
    fingerprint: FINGERPRINT,
    trusted: false,
    previouslyTrusted: false,
    agentId: "helper",
    description: "Writes code",
    projectRoot: "/repo",
    ...overrides,
  };
}

afterEach(() => {
  bridge.native = true;
  bridge.invoke.mockReset();
});

describe("plugin ACP agent service", () => {
  it("prepare sends the reference as a payload and normalizes null lists", async () => {
    bridge.invoke.mockResolvedValueOnce(hostLaunch());
    const launch = await prepareAcpAgent(" acme.tools ", "helper", " /repo ");
    expect(bridge.invoke).toHaveBeenCalledTimes(1);
    expect(bridge.invoke).toHaveBeenCalledWith("plugin_host_acp_agent_prepare", {
      payload: { pluginId: "acme.tools", agentId: "helper", projectRoot: "/repo" },
    });
    expect(launch.pluginFiles).toEqual([]);
    expect(launch.env).toEqual({});
    expect(launch.trusted).toBe(false);
    expect(launch.agentId).toBe("helper");
  });

  it("trust sends the reference and the fingerprint as a payload", async () => {
    const files = [{ relative: "bin/agent", sha256: "c".repeat(64) }];
    bridge.invoke.mockResolvedValueOnce(hostLaunch({ trusted: true, pluginFiles: files }));
    const launch = await trustAcpAgent("acme.tools", "helper", "/repo", FINGERPRINT);
    expect(bridge.invoke).toHaveBeenCalledWith("plugin_host_acp_agent_trust", {
      payload: { pluginId: "acme.tools", agentId: "helper", projectRoot: "/repo", fingerprint: FINGERPRINT },
    });
    expect(launch.trusted).toBe(true);
    expect(launch.pluginFiles).toEqual(files);
  });

  it("acp_launch_plugin takes top-level arguments and never a command", async () => {
    bridge.invoke.mockResolvedValueOnce({ protocolVersion: 1 });
    const result = await launchPluginAcpAgent({ serverId: "conv-1", pluginId: "acme.tools", agentId: "helper", cwd: "/repo" });
    expect(bridge.invoke).toHaveBeenCalledWith("acp_launch_plugin", {
      serverId: "conv-1",
      pluginId: "acme.tools",
      agentId: "helper",
      cwd: "/repo",
    });
    expect(result).toEqual({ protocolVersion: 1 });
  });

  it("rejects an invalid host answer and the browser preview", async () => {
    bridge.invoke.mockResolvedValueOnce(hostLaunch({ fingerprint: "short" }));
    await expect(prepareAcpAgent("acme.tools", "helper", "/repo")).rejects.toThrow(/^unavailable: prepare returned an invalid launch spec/);
    bridge.native = false;
    await expect(prepareAcpAgent("acme.tools", "helper", "/repo")).rejects.toThrow(/^unavailable:/);
    await expect(launchPluginAcpAgent({ serverId: "s", pluginId: "acme.tools", agentId: "helper", cwd: "/repo" })).rejects.toThrow(/^unavailable:/);
    expect(bridge.invoke).toHaveBeenCalledTimes(1);
  });
});
