/**
 * On-demand loading of connector (MCP) tools for the built-in agent loop.
 * Instead of connecting every enabled server and sending all of their tools
 * with every request, a turn offers one small loader tool listing the
 * enabled connector IDs; the model loads one connector's tools when the task
 * needs it. Connectors the conversation already used, or that the request
 * names explicitly, are loaded up front.
 */
import { isDirectConversationIntent } from "./tool-selection";

export const MCP_LOAD_TOOL_NAME = "mcp.load_tools";

const MAX_LISTED_TOOLS = 100;
const MAX_TOOL_DESCRIPTION = 160;

export interface McpToolLoadingPlan {
  /** Offer the loader tool for connectors that are not loaded yet. */
  offerLoader: boolean;
  /** Connectors whose tools are loaded before the first model request. */
  preload: string[];
}

interface HistoryMessage {
  toolResult?: { source?: { kind?: string; serverId?: string } };
}

/** Connectors whose tools (or loader) this conversation already used. */
export function mcpServersFromHistory(history: readonly HistoryMessage[]) {
  const servers = new Set<string>();
  for (const message of history) {
    const source = message.toolResult?.source;
    if (source?.kind === "mcp" && source.serverId?.trim()) servers.add(source.serverId.trim());
  }
  return [...servers];
}

function escapeRegExp(value: string) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/** Connector IDs the request names as a whole word (case-insensitive). */
export function mcpServersMentioned(text: string, serverIds: readonly string[]) {
  return serverIds.filter((id) => {
    const trimmed = id.trim();
    if (!trimmed) return false;
    return new RegExp(`(?:^|[^\\p{L}\\p{N}_-])${escapeRegExp(trimmed)}(?:$|[^\\p{L}\\p{N}_-])`, "iu").test(text);
  });
}

export function planMcpToolLoading(request: {
  text: string;
  serverIds: readonly string[];
  historyServerIds: readonly string[];
}): McpToolLoadingPlan {
  const serverIds = [...new Set(request.serverIds.map((id) => id.trim()).filter(Boolean))];
  if (!serverIds.length || isDirectConversationIntent(request.text)) return { offerLoader: false, preload: [] };
  const wanted = new Set([...request.historyServerIds, ...mcpServersMentioned(request.text, serverIds)]);
  const preload = serverIds.filter((id) => wanted.has(id));
  return { offerLoader: preload.length < serverIds.length, preload };
}

export function mcpLoadToolDefinition(serverIds: readonly string[]) {
  return {
    name: MCP_LOAD_TOOL_NAME,
    description: `Load the tools of one saved connector (MCP server) so they can be called in your next step. Load a connector only when the task needs it. Enabled connectors: ${serverIds.join(", ")}.`,
    inputSchema: {
      type: "object",
      properties: {
        serverId: { type: "string", enum: [...serverIds], description: "Connector to load." },
        toolNames: {
          type: "array",
          items: { type: "string" },
          description: "Optional: load only these tools; omit to load all tools of the connector.",
        },
      },
      required: ["serverId"],
      additionalProperties: false,
    },
  };
}

export function parseMcpLoadRequest(input: Record<string, unknown>, serverIds: readonly string[]) {
  const serverId = typeof input.serverId === "string" ? input.serverId.trim() : "";
  if (!serverIds.includes(serverId)) {
    throw new Error(`Unknown connector "${serverId}". Enabled connectors: ${serverIds.join(", ") || "none"}.`);
  }
  const toolNames = Array.isArray(input.toolNames)
    ? [...new Set(input.toolNames.filter((name): name is string => typeof name === "string").map((name) => name.trim()).filter(Boolean))]
    : [];
  return { serverId, toolNames };
}

/** Keeps the requested tools (all when none are named) and reports names that do not exist. */
export function pickMcpTools<T extends { toolName: string }>(tools: readonly T[], toolNames: readonly string[]) {
  if (!toolNames.length) return { picked: [...tools], unknown: [] as string[] };
  const wanted = new Set(toolNames);
  return {
    picked: tools.filter((tool) => wanted.has(tool.toolName)),
    unknown: toolNames.filter((name) => !tools.some((tool) => tool.toolName === name)),
  };
}

/** Model-visible result of a load: the callable names and short descriptions. */
export function mcpLoadSummary(
  serverId: string,
  tools: readonly { name: string; toolName: string; description: string }[],
  unknown: readonly string[] = [],
) {
  const lines = tools.slice(0, MAX_LISTED_TOOLS).map((tool) => {
    const description = tool.description.replace(/^\[Connector:[^\]]*\]\s*/, "").replace(/\s+/g, " ").trim();
    const short = description.length > MAX_TOOL_DESCRIPTION ? `${description.slice(0, MAX_TOOL_DESCRIPTION - 1)}…` : description;
    return `- ${tool.name}: ${short || tool.toolName}`;
  });
  const more = tools.length > MAX_LISTED_TOOLS ? [`- … ${tools.length - MAX_LISTED_TOOLS} more`] : [];
  const missing = unknown.length ? [`Not found on this connector: ${unknown.join(", ")}.`] : [];
  return [
    tools.length
      ? `Loaded ${tools.length} tool(s) from connector "${serverId}". They are callable from your next step:`
      : `Connector "${serverId}" exposes no matching tools.`,
    ...lines,
    ...more,
    ...missing,
  ].join("\n");
}
