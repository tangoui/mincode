// MCP connector definitions and the helpers that turn a stored server into
// its runtime form. Kept apart from settings-integrations.ts, which also
// carries the skill repository parsers, so the always-loaded settings service
// stays small.

// “连接器”是面向用户的产品名称；MCP 类型与字段继续保留以兼容标准协议和既有配置。
export type McpTransport = "stdio" | "http";

export interface StoredMcpServer {
  id: string;
  transport: McpTransport;
  command: string;
  args: string;
  url: string;
  docsUrl?: string;
  enabled: boolean;
  env: Record<string, string>;
  headers: Record<string, string>;
  /**
   * "oauth": the host signs in to this remote connector and sends the token
   * itself (mcpauth). Only the marker is stored and synced; tokens stay in the
   * Go backend.
   */
  auth?: McpAuthMode;
}

export type McpAuthMode = "oauth";

/** The stored auth marker, or undefined for anything that is not one. */
export function normalizeMcpAuth(value: unknown): McpAuthMode | undefined {
  return value === "oauth" ? value : undefined;
}

const MCP_PATH_PLACEHOLDERS = ["<repository_path>", "<path>"] as const;
export const GIT_MCP_PACKAGE = "mcp-server-git==2026.7.10";
const GIT_MCP_RUNTIME_PREFIX = `--from ${GIT_MCP_PACKAGE} --with mcp<2 mcp-server-git`;

export function normalizeMcpServerForRuntime(
  server: StoredMcpServer,
  workspacePath = ""
): StoredMcpServer {
  let runtimeServer = server;
  if (
    server.transport === "stdio" &&
    server.command.trim() === "uvx" &&
    /^mcp-server-git(?:\s|$)/.test(server.args.trim())
  ) {
    runtimeServer = {
      ...server,
      args: server.args.trim().replace(/^mcp-server-git/, GIT_MCP_RUNTIME_PREFIX),
    };
  }

  const placeholder = findMcpPathPlaceholder(runtimeServer);
  const resolvedPath = workspacePath.trim();
  return placeholder && resolvedPath
    ? { ...runtimeServer, args: runtimeServer.args.split(placeholder).join(resolvedPath) }
    : runtimeServer;
}

export function findMcpPathPlaceholder(server: Pick<StoredMcpServer, "args">) {
  return MCP_PATH_PLACEHOLDERS.find((placeholder) => server.args.includes(placeholder));
}
