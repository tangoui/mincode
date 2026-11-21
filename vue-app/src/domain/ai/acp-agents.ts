// Built-in ACP agent presets. Launch arguments follow the official ACP agent
// registry (cdn.agentclientprotocol.com/registry/v1/latest/registry.json).
// Agents distributed as their own CLI run from PATH so they reuse the user's
// existing login; the adapters for Claude Code, Codex and Pi run through npx.
export interface AcpBuiltinAgent {
  id: string;
  name: string;
  command: string;
  args: readonly string[];
  /** Command shown on the settings card when it differs from the launch line. */
  hint?: string;
}

export interface AcpAgentLaunch {
  command: string;
  args: string[];
}

export const ACP_BUILTIN_AGENTS: readonly AcpBuiltinAgent[] = [
  {
    id: "claude-code",
    name: "Claude Code",
    command: "npx",
    args: ["--yes", "@agentclientprotocol/claude-agent-acp"],
    hint: "claude",
  },
  {
    id: "codex",
    name: "Codex",
    command: "npx",
    args: ["--yes", "@agentclientprotocol/codex-acp"],
    hint: "codex",
  },
  { id: "opencode", name: "OpenCode", command: "opencode", args: ["acp"] },
  { id: "gemini", name: "Gemini CLI", command: "gemini", args: ["--acp"] },
  { id: "qwen-code", name: "Qwen Code", command: "qwen", args: ["--acp"] },
  { id: "goose", name: "goose", command: "goose", args: ["acp"] },
  { id: "auggie", name: "Auggie CLI", command: "auggie", args: ["--acp"] },
  { id: "kimi", name: "Kimi CLI", command: "kimi", args: ["acp"] },
  // pi has no ACP mode of its own; pi-acp drives `pi --mode rpc` and does not
  // forward ACP MCP servers, so pi only sees servers from its own mcp.json.
  { id: "pi", name: "Pi", command: "npx", args: ["--yes", "pi-acp"], hint: "pi" },
];

export function acpBuiltinAgentCommandLine(agent: AcpBuiltinAgent) {
  return agent.hint ?? [agent.command, ...agent.args].join(" ");
}

/** Resolves a built-in adapter ID; unknown IDs fall back to the first preset. */
export function acpBuiltinAgentLaunch(id: string): AcpAgentLaunch {
  const agent = ACP_BUILTIN_AGENTS.find((item) => item.id === id) ?? ACP_BUILTIN_AGENTS[0]!;
  return { command: agent.command, args: [...agent.args] };
}

/**
 * Where the selected agent (`acpAdapterId`) comes from: a built-in preset, a
 * user-configured agent (`custom:<id>`) or a plugin agent
 * (`plugin:<pluginId>/<agentId>`, contributes.acpAgents). A plugin agent is
 * named by reference only; the Go host resolves its launch.
 */
export type AcpAdapterRef =
  | { kind: "builtin"; id: string }
  | { kind: "custom"; id: string }
  | { kind: "plugin"; pluginId: string; agentId: string };

const CUSTOM_ADAPTER_PREFIX = "custom:";
const PLUGIN_ADAPTER_PATTERN = /^plugin:([a-z0-9][a-z0-9._-]{1,127})\/([a-z0-9][a-z0-9-]{0,63})$/;

export function pluginAcpAdapterId(pluginId: string, agentId: string): string {
  return `plugin:${pluginId}/${agentId}`;
}

/** Parses a stored adapter id; anything malformed or unknown is the default built-in agent. */
export function parseAcpAdapterId(id: string): AcpAdapterRef {
  const value = typeof id === "string" ? id.trim() : "";
  const plugin = PLUGIN_ADAPTER_PATTERN.exec(value);
  if (plugin) return { kind: "plugin", pluginId: plugin[1]!, agentId: plugin[2]! };
  if (value.startsWith(CUSTOM_ADAPTER_PREFIX) && value.length > CUSTOM_ADAPTER_PREFIX.length) {
    return { kind: "custom", id: value.slice(CUSTOM_ADAPTER_PREFIX.length) };
  }
  const builtin = ACP_BUILTIN_AGENTS.find((agent) => agent.id === value) ?? ACP_BUILTIN_AGENTS[0]!;
  return { kind: "builtin", id: builtin.id };
}
