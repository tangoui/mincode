import { normalizeMcpServerForRuntime, type StoredMcpServer } from "@/domain/ai/mcp-servers";

export interface AcpMcpServer {
  type: "stdio" | "http";
  name: string;
  command?: string;
  args?: string[];
  env?: Array<{ name: string; value: string }>;
  url?: string;
  headers?: Array<{ name: string; value: string }>;
}

function stringRecord(value: unknown) {
  if (!value || typeof value !== "object" || Array.isArray(value)) return {} as Record<string, string>;
  return Object.fromEntries(
    Object.entries(value).filter((entry): entry is [string, string] => typeof entry[1] === "string"),
  );
}

function pairs(record: Record<string, string>) {
  return Object.entries(record).map(([name, value]) => ({ name, value }));
}

export function buildAcpMcpServers(
  stored: readonly StoredMcpServer[],
  projectRoot: string,
): AcpMcpServer[] {
  const servers: AcpMcpServer[] = [];
  for (const configured of stored) {
    if (!configured?.id?.trim() || configured.enabled === false) continue;
    const server = normalizeMcpServerForRuntime(configured, projectRoot);
    const name = server.id.trim();
    if (server.transport === "http") {
      if (server.url?.trim()) {
        servers.push({ type: "http", name, url: server.url.trim(), headers: pairs(stringRecord(server.headers)) });
      }
    } else if (server.command?.trim()) {
      servers.push({
        type: "stdio",
        name,
        command: server.command.trim(),
        args: server.args?.trim() ? server.args.trim().split(/\s+/) : [],
        env: pairs(stringRecord(server.env)),
      });
    }
  }
  return servers;
}
