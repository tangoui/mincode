/**
 * Which ACP agent a conversation's connection starts, and how a plugin agent
 * (contributes.acpAgents) is confirmed and launched by reference.
 *
 * A plugin agent is named by `(pluginId, agentId)` only: the host resolves the
 * launch (prepare), records the user's confirmation of exactly that launch
 * (trust) and refuses acp_launch_plugin for anything else with `untrusted:`;
 * `unavailable:` means the plugin or entry is gone. Whether an agent is gone is
 * decided by the host's answer, or by a missing entry once the plugin list is
 * known (`ready`), never by a list that is still loading.
 *
 * A conversation records the adapter that started it (`acpAdapterId`). Its
 * previous ACP session is never loaded by a different agent when a plugin
 * agent is on either side; built-in and custom agents behave as before, and a
 * conversation without a record matches any agent. When the selected plugin
 * agent is gone, new sessions fall back to the default built-in agent; the
 * selection in Settings is kept.
 */
import { resolvePluginTitle } from "@/domain/plugins/title";
import { acpAgentErrorCode } from "@/domain/plugins/acp-agents";
import { ACP_BUILTIN_AGENTS, parseAcpAdapterId } from "./acp-agents";

export const DEFAULT_ACP_ADAPTER_ID = ACP_BUILTIN_AGENTS[0]!.id;

/** The stored selection, trimmed; an empty one selects the default built-in agent. */
export function selectedAcpAdapterId(stored: string | undefined): string {
  return (typeof stored === "string" ? stored.trim() : "") || DEFAULT_ACP_ADAPTER_ID;
}

/**
 * True when a session recorded under `recorded` must not be loaded by
 * `adapterId`: the two differ and one of them is a plugin agent.
 */
export function acpSessionBelongsElsewhere(recorded: string | undefined, adapterId: string): boolean {
  const from = typeof recorded === "string" ? recorded.trim() : "";
  if (!from || from === adapterId) return false;
  return parseAcpAdapterId(from).kind === "plugin" || parseAcpAdapterId(adapterId).kind === "plugin";
}

export interface AcpAgentStartRequest {
  /** acpAdapterId from Settings. */
  selectedAdapterId: string | undefined;
  /** The adapter the conversation recorded, if any. */
  recordedAdapterId: string | undefined;
  /** The conversation has an ACP session the caller would load. */
  hasPreviousSession: boolean;
  serverId: string;
  projectRoot: string;
  /** The plugin agent list; `ready` false means it is not known yet. */
  pluginAgents: { ready: boolean; agents: readonly { pluginId: string; agentId: string }[] };
}

interface PluginLaunchState {
  trusted: boolean;
  fingerprint: string;
}

export interface AcpAgentStartDeps<L extends PluginLaunchState> {
  /** Starts a built-in or custom agent (acp_launch). */
  launchCommandAgent(adapterId: string): Promise<void>;
  prepare(pluginId: string, agentId: string, projectRoot: string): Promise<L>;
  trust(pluginId: string, agentId: string, projectRoot: string, fingerprint: string): Promise<L>;
  launchPlugin(input: { serverId: string; pluginId: string; agentId: string; cwd: string }): Promise<unknown>;
  /** Shows the confirmation; true only when the user chose to run it. */
  confirm(launch: L): Promise<boolean>;
}

export type AcpAgentStartOutcome =
  /** The agent process runs; record adapterId. fallbackFrom: the selected plugin agent that was gone. */
  | { kind: "started"; adapterId: string; fallbackFrom?: string }
  /** The conversation's session belongs to another agent; nothing was started. */
  | { kind: "conversation-unavailable"; recordedAdapterId: string; adapterId: string }
  /** The user declined the confirmation. */
  | { kind: "cancelled"; adapterId: string }
  /** The agent changed again after a second confirmation. */
  | { kind: "changed"; adapterId: string };

function isRetryable(error: unknown) {
  const code = acpAgentErrorCode(error);
  return code === "untrusted" || code === "changed";
}

/**
 * Starts the conversation's agent process (not its session). The user drives
 * every confirmation; a launch that changed after the confirmation is
 * prepared and confirmed once more before giving up.
 */
export async function startAcpAgent<L extends PluginLaunchState>(
  request: AcpAgentStartRequest,
  deps: AcpAgentStartDeps<L>,
): Promise<AcpAgentStartOutcome> {
  const selected = selectedAcpAdapterId(request.selectedAdapterId);
  const recorded = request.recordedAdapterId?.trim() ?? "";
  const blocked = (adapterId: string) => request.hasPreviousSession && acpSessionBelongsElsewhere(recorded, adapterId);
  const startCommand = async (adapterId: string, fallbackFrom?: string): Promise<AcpAgentStartOutcome> => {
    if (blocked(adapterId)) return { kind: "conversation-unavailable", recordedAdapterId: recorded, adapterId };
    await deps.launchCommandAgent(adapterId);
    return fallbackFrom ? { kind: "started", adapterId, fallbackFrom } : { kind: "started", adapterId };
  };

  const ref = parseAcpAdapterId(selected);
  if (ref.kind !== "plugin") return startCommand(selected);
  const fallback = () => startCommand(DEFAULT_ACP_ADAPTER_ID, selected);
  const { pluginId, agentId } = ref;
  const { projectRoot, serverId } = request;
  const listed = request.pluginAgents.agents.some((agent) => agent.pluginId === pluginId && agent.agentId === agentId);
  if (request.pluginAgents.ready && !listed) return fallback();

  const prepare = () => deps.prepare(pluginId, agentId, projectRoot);
  let launch: L;
  try {
    launch = await prepare();
  } catch (error) {
    if (acpAgentErrorCode(error) === "unavailable") return fallback();
    throw error;
  }
  // Checked after prepare: a gone agent falls back, and the fallback may still match.
  if (blocked(selected)) return { kind: "conversation-unavailable", recordedAdapterId: recorded, adapterId: selected };

  for (let attempt = 0; ; attempt++) {
    try {
      if (!launch.trusted) {
        if (!(await deps.confirm(launch))) return { kind: "cancelled", adapterId: selected };
        launch = await deps.trust(pluginId, agentId, projectRoot, launch.fingerprint);
      }
      await deps.launchPlugin({ serverId, pluginId, agentId, cwd: projectRoot });
      return { kind: "started", adapterId: selected };
    } catch (error) {
      if (acpAgentErrorCode(error) === "unavailable") return fallback();
      if (!isRetryable(error)) throw error;
      if (attempt >= 1) return { kind: "changed", adapterId: selected };
    }
    try {
      launch = await prepare();
    } catch (error) {
      if (acpAgentErrorCode(error) === "unavailable") return fallback();
      throw error;
    }
  }
}

/** Returns true the first time it sees a key (per app session when module-scoped). */
export function createOnceGate() {
  const seen = new Set<string>();
  return (key: string) => {
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  };
}

export interface AcpAdapterLabelContext {
  pluginAgents: readonly { pluginId: string; agentId: string; title: string; titleLocalized?: Record<string, string> }[];
  customAgents: readonly { id: string; name: string }[] | undefined;
  localeChain: readonly string[];
}

/** A display name for an adapter id; a gone plugin agent shows as `pluginId/agentId`. */
export function acpAdapterLabel(adapterId: string, context: AcpAdapterLabelContext): string {
  const ref = parseAcpAdapterId(adapterId);
  if (ref.kind === "plugin") {
    const agent = context.pluginAgents.find((item) => item.pluginId === ref.pluginId && item.agentId === ref.agentId);
    return agent ? resolvePluginTitle(agent, context.localeChain) : `${ref.pluginId}/${ref.agentId}`;
  }
  if (ref.kind === "custom") {
    return context.customAgents?.find((item) => item.id === ref.id)?.name?.trim() || ref.id;
  }
  return ACP_BUILTIN_AGENTS.find((agent) => agent.id === ref.id)?.name ?? ref.id;
}
