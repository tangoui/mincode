import { describe, expect, it, vi } from "vitest";
import {
  acpAdapterLabel,
  acpSessionBelongsElsewhere,
  createOnceGate,
  DEFAULT_ACP_ADAPTER_ID,
  selectedAcpAdapterId,
  startAcpAgent,
  type AcpAgentStartDeps,
  type AcpAgentStartRequest,
} from "@/domain/ai/acp-agent-start";

const PLUGIN_ID = "plugin:acme.agents/helper";
const FINGERPRINT = "a".repeat(64);

interface FakeLaunch {
  trusted: boolean;
  fingerprint: string;
}

function request(overrides: Partial<AcpAgentStartRequest> = {}): AcpAgentStartRequest {
  return {
    selectedAdapterId: PLUGIN_ID,
    recordedAdapterId: undefined,
    hasPreviousSession: false,
    serverId: "server-1",
    projectRoot: "/repo",
    pluginAgents: { ready: true, agents: [{ pluginId: "acme.agents", agentId: "helper" }] },
    ...overrides,
  };
}

function deps(overrides: Partial<AcpAgentStartDeps<FakeLaunch>> = {}) {
  const base = {
    launchCommandAgent: vi.fn(async (_adapterId: string) => {}),
    prepare: vi.fn(async (): Promise<FakeLaunch> => ({ trusted: false, fingerprint: FINGERPRINT })),
    trust: vi.fn(async (): Promise<FakeLaunch> => ({ trusted: true, fingerprint: FINGERPRINT })),
    launchPlugin: vi.fn(async () => ({})),
    confirm: vi.fn(async (_launch: FakeLaunch) => true),
  };
  return { ...base, ...overrides } as typeof base;
}

describe("selectedAcpAdapterId / acpSessionBelongsElsewhere", () => {
  it("defaults an empty selection to the first built-in agent", () => {
    expect(selectedAcpAdapterId(undefined)).toBe(DEFAULT_ACP_ADAPTER_ID);
    expect(selectedAcpAdapterId("  ")).toBe(DEFAULT_ACP_ADAPTER_ID);
    expect(selectedAcpAdapterId(" codex ")).toBe("codex");
  });

  it("keeps built-in and custom agents as before and treats a missing record as a match", () => {
    expect(acpSessionBelongsElsewhere(undefined, PLUGIN_ID)).toBe(false);
    expect(acpSessionBelongsElsewhere("", PLUGIN_ID)).toBe(false);
    expect(acpSessionBelongsElsewhere("claude-code", "codex")).toBe(false);
    expect(acpSessionBelongsElsewhere("custom:mine", "codex")).toBe(false);
    expect(acpSessionBelongsElsewhere(PLUGIN_ID, PLUGIN_ID)).toBe(false);
  });

  it("never hands a session to another agent when a plugin agent is involved", () => {
    expect(acpSessionBelongsElsewhere(PLUGIN_ID, "claude-code")).toBe(true);
    expect(acpSessionBelongsElsewhere("claude-code", PLUGIN_ID)).toBe(true);
    expect(acpSessionBelongsElsewhere(PLUGIN_ID, "plugin:acme.agents/other")).toBe(true);
  });
});

describe("startAcpAgent", () => {
  it("starts built-in and custom agents through the command launch, as before", async () => {
    const d = deps();
    await expect(startAcpAgent(request({ selectedAdapterId: "codex" }), d)).resolves.toEqual({ kind: "started", adapterId: "codex" });
    await expect(
      startAcpAgent(request({ selectedAdapterId: "custom:mine", recordedAdapterId: "codex", hasPreviousSession: true }), d),
    ).resolves.toEqual({ kind: "started", adapterId: "custom:mine" });
    expect(d.launchCommandAgent.mock.calls).toEqual([["codex"], ["custom:mine"]]);
    expect(d.prepare).not.toHaveBeenCalled();
  });

  it("asks the user before trusting and launching an unconfirmed plugin agent", async () => {
    const d = deps();
    const outcome = await startAcpAgent(request(), d);
    expect(outcome).toEqual({ kind: "started", adapterId: PLUGIN_ID });
    expect(d.prepare).toHaveBeenCalledWith("acme.agents", "helper", "/repo");
    expect(d.confirm).toHaveBeenCalledTimes(1);
    expect(d.trust).toHaveBeenCalledWith("acme.agents", "helper", "/repo", FINGERPRINT);
    expect(d.launchPlugin).toHaveBeenCalledWith({ serverId: "server-1", pluginId: "acme.agents", agentId: "helper", cwd: "/repo" });
    expect(d.launchCommandAgent).not.toHaveBeenCalled();
  });

  it("launches a confirmed plugin agent without asking", async () => {
    const d = deps({ prepare: vi.fn(async () => ({ trusted: true, fingerprint: FINGERPRINT })) });
    await expect(startAcpAgent(request(), d)).resolves.toEqual({ kind: "started", adapterId: PLUGIN_ID });
    expect(d.confirm).not.toHaveBeenCalled();
    expect(d.trust).not.toHaveBeenCalled();
  });

  it("does not trust or launch when the user cancels", async () => {
    const d = deps({ confirm: vi.fn(async () => false) });
    await expect(startAcpAgent(request(), d)).resolves.toEqual({ kind: "cancelled", adapterId: PLUGIN_ID });
    expect(d.trust).not.toHaveBeenCalled();
    expect(d.launchPlugin).not.toHaveBeenCalled();
    expect(d.launchCommandAgent).not.toHaveBeenCalled();
  });

  it("asks again once when the agent changed after the confirmation, then gives up", async () => {
    const d = deps({ launchPlugin: vi.fn(async () => Promise.reject(new Error("untrusted: ACP agent changed"))) });
    await expect(startAcpAgent(request(), d)).resolves.toEqual({ kind: "changed", adapterId: PLUGIN_ID });
    expect(d.prepare).toHaveBeenCalledTimes(2);
    expect(d.confirm).toHaveBeenCalledTimes(2);
    expect(d.launchPlugin).toHaveBeenCalledTimes(2);
  });

  it("re-prepares when the trust call reports a changed fingerprint", async () => {
    const trust = vi
      .fn()
      .mockRejectedValueOnce(new Error("changed: ACP agent changed since it was reviewed"))
      .mockResolvedValue({ trusted: true, fingerprint: FINGERPRINT });
    const d = deps({ trust });
    await expect(startAcpAgent(request(), d)).resolves.toEqual({ kind: "started", adapterId: PLUGIN_ID });
    expect(d.confirm).toHaveBeenCalledTimes(2);
    expect(d.launchPlugin).toHaveBeenCalledTimes(1);
  });

  it("falls back to the default built-in agent for a new conversation when the host says the agent is gone", async () => {
    const d = deps({ prepare: vi.fn(async () => Promise.reject(new Error("unavailable: plugin is disabled"))) });
    const outcome = await startAcpAgent(request({ pluginAgents: { ready: false, agents: [] } }), d);
    expect(outcome).toEqual({ kind: "started", adapterId: DEFAULT_ACP_ADAPTER_ID, fallbackFrom: PLUGIN_ID });
    expect(d.launchCommandAgent).toHaveBeenCalledWith(DEFAULT_ACP_ADAPTER_ID);
    expect(d.launchPlugin).not.toHaveBeenCalled();
  });

  it("falls back without asking the host once the list is known and the agent is missing", async () => {
    const d = deps();
    const outcome = await startAcpAgent(request({ pluginAgents: { ready: true, agents: [] } }), d);
    expect(outcome).toEqual({ kind: "started", adapterId: DEFAULT_ACP_ADAPTER_ID, fallbackFrom: PLUGIN_ID });
    expect(d.prepare).not.toHaveBeenCalled();
  });

  it("asks the host instead of trusting a list that is not known yet", async () => {
    const d = deps({ prepare: vi.fn(async () => ({ trusted: true, fingerprint: FINGERPRINT })) });
    const outcome = await startAcpAgent(request({ pluginAgents: { ready: false, agents: [] } }), d);
    expect(outcome).toEqual({ kind: "started", adapterId: PLUGIN_ID });
    expect(d.prepare).toHaveBeenCalledTimes(1);
  });

  it("falls back when the agent disappears between confirmation and launch", async () => {
    const d = deps({ launchPlugin: vi.fn(async () => Promise.reject(new Error("unavailable: plugin uninstalled"))) });
    const outcome = await startAcpAgent(request(), d);
    expect(outcome).toEqual({ kind: "started", adapterId: DEFAULT_ACP_ADAPTER_ID, fallbackFrom: PLUGIN_ID });
  });

  it("leaves a conversation of a gone plugin agent unavailable instead of loading it elsewhere", async () => {
    const d = deps({ prepare: vi.fn(async () => Promise.reject(new Error("unavailable: plugin is disabled"))) });
    const outcome = await startAcpAgent(
      request({ recordedAdapterId: PLUGIN_ID, hasPreviousSession: true, pluginAgents: { ready: false, agents: [] } }),
      d,
    );
    expect(outcome).toEqual({ kind: "conversation-unavailable", recordedAdapterId: PLUGIN_ID, adapterId: DEFAULT_ACP_ADAPTER_ID });
    expect(d.launchCommandAgent).not.toHaveBeenCalled();
    expect(d.launchPlugin).not.toHaveBeenCalled();
  });

  it("leaves a plugin agent's conversation unavailable when another agent is selected now", async () => {
    const d = deps();
    const outcome = await startAcpAgent(
      request({ selectedAdapterId: "codex", recordedAdapterId: PLUGIN_ID, hasPreviousSession: true }),
      d,
    );
    expect(outcome).toEqual({ kind: "conversation-unavailable", recordedAdapterId: PLUGIN_ID, adapterId: "codex" });
    expect(d.launchCommandAgent).not.toHaveBeenCalled();
  });

  it("does not hand a built-in agent's session to a plugin agent, without a confirmation", async () => {
    const d = deps();
    const outcome = await startAcpAgent(request({ recordedAdapterId: "claude-code", hasPreviousSession: true }), d);
    expect(outcome).toEqual({ kind: "conversation-unavailable", recordedAdapterId: "claude-code", adapterId: PLUGIN_ID });
    expect(d.confirm).not.toHaveBeenCalled();
    expect(d.launchPlugin).not.toHaveBeenCalled();
  });

  it("keeps using the fallback for a conversation it started while the plugin agent is still gone", async () => {
    const d = deps();
    const outcome = await startAcpAgent(
      request({ recordedAdapterId: DEFAULT_ACP_ADAPTER_ID, hasPreviousSession: true, pluginAgents: { ready: true, agents: [] } }),
      d,
    );
    expect(outcome).toEqual({ kind: "started", adapterId: DEFAULT_ACP_ADAPTER_ID, fallbackFrom: PLUGIN_ID });
  });

  it("rethrows other host errors", async () => {
    const d = deps({ prepare: vi.fn(async () => Promise.reject(new Error("forbidden: shell capability revoked"))) });
    await expect(startAcpAgent(request(), d)).rejects.toThrow("forbidden:");
    expect(d.launchCommandAgent).not.toHaveBeenCalled();
  });
});

describe("createOnceGate", () => {
  it("opens once per key", () => {
    const gate = createOnceGate();
    expect(gate(PLUGIN_ID)).toBe(true);
    expect(gate(PLUGIN_ID)).toBe(false);
    expect(gate("plugin:acme.agents/other")).toBe(true);
  });
});

describe("acpAdapterLabel", () => {
  const context = {
    pluginAgents: [{ pluginId: "acme.agents", agentId: "helper", title: "Helper", titleLocalized: { zh: "助手" } }],
    customAgents: [{ id: "mine", name: "My Agent" }],
    localeChain: ["zh"],
  };
  it("names built-in, custom and plugin agents", () => {
    expect(acpAdapterLabel("codex", context)).toBe("Codex");
    expect(acpAdapterLabel("custom:mine", context)).toBe("My Agent");
    expect(acpAdapterLabel(PLUGIN_ID, context)).toBe("助手");
    expect(acpAdapterLabel("plugin:acme.agents/gone", context)).toBe("acme.agents/gone");
  });
});
