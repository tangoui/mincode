import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";
import {
  findMcpPathPlaceholder,
  GIT_MCP_PACKAGE,
  type McpTransport,
  type StoredMcpServer,
} from "@/domain/ai/mcp-servers";

export {
  findMcpPathPlaceholder,
  normalizeMcpServerForRuntime,
  type McpTransport,
  type StoredMcpServer,
} from "@/domain/ai/mcp-servers";

export function resolveMcpPathPlaceholder(server: StoredMcpServer, path: string) {
  const placeholder = findMcpPathPlaceholder(server);
  return placeholder
    ? { ...server, args: server.args.replace(placeholder, path) }
    : server;
}

export function mergeMcpServersById(
  existing: StoredMcpServer[],
  incoming: StoredMcpServer[]
) {
  const merged = [...existing];
  for (const server of incoming) {
    const index = merged.findIndex((item) => item.id === server.id);
    if (index >= 0) merged.splice(index, 1, server);
    else merged.push(server);
  }
  return merged;
}

export interface RegistryEntry {
  id: string;
  name: string;
  description: string;
  source: "builtin" | "git" | "registry";
  installRef?: string;
  docsUrl?: string;
  inlinePayload?: string;
}

interface HttpResponse {
  status: number;
  body: string;
}

// Discover lists the built-in catalog plus plugin catalogs (contributes.aiCatalogs).
// Plugin sources are supplied by the connector catalog plugin (`ai-market`), which also handles upgrades.

const BUILTIN_MCP_CATALOG: RegistryEntry[] = [
  {
    id: "builtin-mcp-filesystem",
    docsUrl: "https://github.com/modelcontextprotocol/servers/tree/main/src/filesystem",
    name: "File System",
    description: "Read and write local files with an explicitly allowed directory.",
    source: "builtin",
    inlinePayload: JSON.stringify({
      name: "filesystem",
      transport: "stdio",
      command: "npx",
      args: ["-y", "@modelcontextprotocol/server-filesystem", "<path>"],
    }),
  },
  {
    id: "builtin-mcp-github",
    docsUrl: "https://github.com/modelcontextprotocol/servers-archived/tree/main/src/github",
    name: "GitHub",
    description: "Work with GitHub repositories, issues, and pull requests (PAT required).",
    source: "builtin",
    inlinePayload: JSON.stringify({
      name: "github",
      transport: "stdio",
      command: "npx",
      args: ["-y", "@modelcontextprotocol/server-github"],
      env: { GITHUB_PERSONAL_ACCESS_TOKEN: "" },
    }),
  },
  {
    id: "builtin-mcp-postgres",
    docsUrl: "https://github.com/modelcontextprotocol/servers-archived/tree/main/src/postgres",
    name: "PostgreSQL",
    description: "Query PostgreSQL with a connection string you provide.",
    source: "builtin",
    inlinePayload: JSON.stringify({
      name: "postgres",
      transport: "stdio",
      command: "npx",
      args: ["-y", "@modelcontextprotocol/server-postgres", "<connection_string>"],
    }),
  },
  {
    id: "builtin-mcp-playwright",
    docsUrl: "https://github.com/microsoft/playwright-mcp",
    name: "Playwright",
    description: "Automate browser inspection and interaction through Playwright MCP.",
    source: "builtin",
    inlinePayload: JSON.stringify({
      name: "playwright",
      transport: "stdio",
      command: "npx",
      args: ["-y", "@playwright/mcp@latest"],
    }),
  },
  {
    id: "builtin-mcp-memory",
    docsUrl: "https://github.com/modelcontextprotocol/servers/tree/main/src/memory",
    name: "Memory",
    description: "Keep an editable local knowledge graph for the AI assistant.",
    source: "builtin",
    inlinePayload: JSON.stringify({
      name: "memory",
      transport: "stdio",
      command: "npx",
      args: ["-y", "@modelcontextprotocol/server-memory"],
    }),
  },
  {
    id: "builtin-mcp-sequential-thinking",
    docsUrl: "https://github.com/modelcontextprotocol/servers/tree/main/src/sequentialthinking",
    name: "Sequential Thinking",
    description: "Provide structured, revisable reasoning steps for complex tasks.",
    source: "builtin",
    inlinePayload: JSON.stringify({
      name: "sequential-thinking",
      transport: "stdio",
      command: "npx",
      args: ["-y", "@modelcontextprotocol/server-sequential-thinking"],
    }),
  },
  {
    id: "builtin-mcp-context7",
    docsUrl: "https://github.com/upstash/context7",
    name: "Context7",
    description: "Retrieve current library and framework documentation for coding tasks.",
    source: "builtin",
    inlinePayload: JSON.stringify({
      name: "context7",
      transport: "stdio",
      command: "npx",
      args: ["-y", "@upstash/context7-mcp@latest"],
    }),
  },
  {
    id: "builtin-mcp-fetch",
    docsUrl: "https://github.com/modelcontextprotocol/servers/tree/main/src/fetch",
    name: "Web Fetch",
    description: "Fetch and extract text from web pages and HTTP endpoints (requires uvx).",
    source: "builtin",
    inlinePayload: JSON.stringify({
      name: "web-fetch",
      transport: "stdio",
      command: "uvx",
      // mcp-server-fetch 仍导入旧 SDK 的 McpError，与 mcp>=2 不兼容，同 Git 预置一样锁定 1.x。
      args: ["--from", "mcp-server-fetch", "--with", "mcp<2", "mcp-server-fetch"],
    }),
  },
  {
    id: "builtin-mcp-git",
    docsUrl: "https://github.com/modelcontextprotocol/servers/tree/main/src/git",
    name: "Git",
    description: "Inspect a local Git repository you explicitly select (requires uvx).",
    source: "builtin",
    inlinePayload: JSON.stringify({
      name: "git",
      transport: "stdio",
      command: "uvx",
      args: [
        "--from",
        GIT_MCP_PACKAGE,
        "--with",
        "mcp<2",
        "mcp-server-git",
        "--repository",
        "<repository_path>",
      ],
    }),
  },
];

/** Returns the curated MCP templates without installing, connecting, or executing them. */
export function getBuiltinMcpCatalog() {
  return BUILTIN_MCP_CATALOG;
}

function asString(value: unknown) {
  return typeof value === "string" ? value.trim() : "";
}

/** Keeps documentation links inert unless they use a regular web protocol. */
export function normalizeDocumentationUrl(value: unknown) {
  try {
    const url = new URL(asString(value));
    if (url.protocol !== "https:" && url.protocol !== "http:") return "";
    return url.toString();
  } catch {
    return "";
  }
}

function asStringRecord(value: unknown): Record<string, string> {
  if (!value || typeof value !== "object" || Array.isArray(value)) return {};
  return Object.fromEntries(
    Object.entries(value).filter(
      (entry): entry is [string, string] => typeof entry[1] === "string" && Boolean(entry[0].trim())
    )
  );
}

function asArgs(value: unknown) {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === "string")
    : [];
}

function slugify(value: string, fallback: string) {
  return (
    value
      .trim()
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 48) || fallback
  );
}

function filenameWithoutExtension(path: string, fallback: string) {
  const filename = path.split(/[\\/]/).filter(Boolean).pop() || fallback;
  return filename.replace(/\.[^.]+$/, "") || fallback;
}

export function toRawContentUrl(value: string) {
  try {
    const url = new URL(value.trim());
    if (url.protocol !== "https:" && url.protocol !== "http:") return null;
    if (url.hostname === "github.com") {
      const parts = url.pathname.split("/").filter(Boolean);
      if (parts[2] === "blob" && parts.length > 3) {
        return `https://raw.githubusercontent.com/${parts[0]}/${parts[1]}/${parts.slice(3).join("/")}`;
      }
    }
    return url.toString();
  } catch {
    return null;
  }
}

async function fetchRemoteText(url: string) {
  if (isDesktopRuntime()) {
    const response = await invokeBackend<HttpResponse>("http_send_request", {
      payload: { method: "GET", url, headers: [], timeoutMs: 15_000 },
    });
    if (response.status < 200 || response.status >= 300) throw new Error(`HTTP ${response.status}`);
    return response.body;
  }

  const response = await fetch(url);
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  return response.text();
}

function matches(entry: RegistryEntry, query: string) {
  const needle = query.trim().toLowerCase();
  return !needle || `${entry.name} ${entry.description}`.toLowerCase().includes(needle);
}

function directUrlEntry(value: string): RegistryEntry | null {
  const installRef = toRawContentUrl(value);
  if (!installRef || !installRef.toLowerCase().split(/[?#]/)[0]!.endsWith(".json")) return null;
  return {
    id: `git-${installRef}`,
    name: filenameWithoutExtension(installRef, "MCP Server"),
    description: installRef,
    source: "git",
    installRef,
    docsUrl: normalizeDocumentationUrl(value),
  };
}

function mergeEntries(...lists: RegistryEntry[][]) {
  const seen = new Set<string>();
  return lists.flat().filter((entry) => {
    if (seen.has(entry.id)) return false;
    seen.add(entry.id);
    return true;
  });
}

export async function searchMcpRegistry(query: string) {
  const direct = directUrlEntry(query);
  return mergeEntries(
    direct ? [direct] : [],
    BUILTIN_MCP_CATALOG.filter((entry) => matches(entry, query)),
  );
}

function normaliseMcpServer(id: string, value: unknown): StoredMcpServer | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const raw = value as Record<string, unknown>;
  const url = asString(raw.url);
  const transport: McpTransport =
    url || raw.transport === "http" || raw.type === "http" || raw.type === "streamable-http"
      ? "http"
      : "stdio";
  const command = asString(raw.command);
  if ((transport === "stdio" && !command) || (transport === "http" && !url)) return null;
  return {
    id: slugify(id || asString(raw.name), `mcp-${Date.now()}`),
    transport,
    command,
    args: asArgs(raw.args).join(" "),
    url,
    docsUrl: normalizeDocumentationUrl(
      raw.docsUrl || raw.documentationUrl || raw.documentation || raw.docs || raw.homepage
    ),
    enabled: raw.enabled !== false,
    env: asStringRecord(raw.env),
    headers: asStringRecord(raw.headers),
  };
}

/**
 * A connector read from saved settings: documentation link made inert unless
 * it is a web URL, env/headers defaulted, and legacy authentication markers discarded.
 */
export function normalizeStoredMcpServer(server: StoredMcpServer): StoredMcpServer {
  const rest = { ...server } as Partial<StoredMcpServer> & { auth?: unknown };
  delete rest.auth;
  return {
    ...rest,
    docsUrl: normalizeDocumentationUrl(rest.docsUrl),
    env: rest.env && typeof rest.env === "object" ? rest.env : {},
    headers: rest.headers && typeof rest.headers === "object" ? rest.headers : {},
  } as StoredMcpServer;
}

/**
 * Fields an external sync (domain/ai/external-sync) stores on a settings
 * object. They are not part of any editor, so an edit must carry them over and
 * a copy must drop them: two objects never share one sync source.
 */
const SYNC_METADATA_FIELDS = ["syncSource"] as const;

/** A copy of `value` without sync metadata, for duplicating a settings object. */
export function withoutSyncMetadata<T extends object>(value: T): T {
  const copy = { ...(value as Record<string, unknown>) };
  for (const field of SYNC_METADATA_FIELDS) delete copy[field];
  return copy as T;
}

/** `next` with the sync metadata of the object in `previous` that has the same id. */
export function keepSyncMetadata<T extends { id: string }>(next: readonly T[], previous: readonly object[]): T[] {
  const previousById = new Map<string, Record<string, unknown>>();
  for (const item of previous as readonly Record<string, unknown>[]) {
    if (typeof item.id === "string") previousById.set(item.id, item);
  }
  return next.map((item) => {
    const before = previousById.get(item.id);
    if (!before) return item;
    const metadata = Object.fromEntries(
      SYNC_METADATA_FIELDS.filter((field) => before[field] !== undefined).map((field) => [field, before[field]]),
    );
    return { ...item, ...metadata };
  });
}

export function serializeEditableMcpConfig(servers: readonly StoredMcpServer[]) {
  const mcpServers = Object.fromEntries(
    servers.map((server) => {
      const config: Record<string, unknown> = server.transport === "http"
        ? { type: "http", url: server.url }
        : {
            command: server.command,
            args: server.args.trim() ? server.args.trim().split(/\s+/) : [],
          };
      if (!server.enabled) config.enabled = false;
      if (server.docsUrl) config.docsUrl = server.docsUrl;
      if (Object.keys(server.env).length) config.env = { ...server.env };
      if (Object.keys(server.headers).length) config.headers = { ...server.headers };
      return [server.id, config];
    }),
  );
  return JSON.stringify({ mcpServers }, null, 2);
}

function strictStringRecord(value: unknown, field: string) {
  if (value === undefined) return {};
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new Error(`invalid_mcp_${field}`);
  }
  const entries = Object.entries(value);
  if (entries.some(([key, item]) => !key.trim() || typeof item !== "string")) {
    throw new Error(`invalid_mcp_${field}`);
  }
  return Object.fromEntries(entries) as Record<string, string>;
}

/** Strict parser used by the in-app JSON editor. It never skips invalid entries. */
export function parseEditableMcpConfig(text: string): StoredMcpServer[] {
  const parsed: unknown = JSON.parse(text);
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error("invalid_mcp_config");
  }
  const root = parsed as Record<string, unknown>;
  if (!root.mcpServers || typeof root.mcpServers !== "object" || Array.isArray(root.mcpServers)) {
    throw new Error("invalid_mcp_servers");
  }
  return Object.entries(root.mcpServers as Record<string, unknown>).map(([rawId, value]) => {
    const id = rawId.trim();
    if (!id || !value || typeof value !== "object" || Array.isArray(value)) {
      throw new Error("invalid_mcp_server");
    }
    const config = value as Record<string, unknown>;
    const url = asString(config.url);
    const transport: McpTransport =
      url || config.transport === "http" || config.type === "http" || config.type === "streamable-http"
        ? "http"
        : "stdio";
    const command = asString(config.command);
    if ((transport === "stdio" && !command) || (transport === "http" && !url)) {
      throw new Error(`invalid_mcp_server:${id}`);
    }
    if (config.args !== undefined && (!Array.isArray(config.args) || config.args.some((item) => typeof item !== "string"))) {
      throw new Error(`invalid_mcp_args:${id}`);
    }
    if (config.enabled !== undefined && typeof config.enabled !== "boolean") {
      throw new Error(`invalid_mcp_enabled:${id}`);
    }
    const rawDocsUrl =
      config.docsUrl ?? config.documentationUrl ?? config.documentation ?? config.docs ?? config.homepage;
    const docsUrl = rawDocsUrl === undefined ? "" : normalizeDocumentationUrl(rawDocsUrl);
    if (rawDocsUrl !== undefined && !docsUrl) throw new Error(`invalid_mcp_docs_url:${id}`);
    return {
      id,
      transport,
      command,
      args: asArgs(config.args).join(" "),
      url,
      ...(docsUrl ? { docsUrl } : {}),
      enabled: config.enabled !== false,
      env: strictStringRecord(config.env, "env"),
      headers: strictStringRecord(config.headers, "headers"),
    };
  });
}

/** Parses Cursor/Claude-style mcp.json files without executing commands. */
export function parseMcpConfig(text: string) {
  const parsed: unknown = JSON.parse(text);
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error("invalid_mcp_config");
  }
  const root = parsed as Record<string, unknown>;
  const hasDirectServerFields = Boolean(asString(root.command) || asString(root.url));
  const source =
    root.mcpServers && typeof root.mcpServers === "object" && !Array.isArray(root.mcpServers)
      ? (root.mcpServers as Record<string, unknown>)
      : root;
  const servers = hasDirectServerFields
    ? [normaliseMcpServer(asString(root.name) || "mcp-server", root)].filter(
        (server): server is StoredMcpServer => Boolean(server)
      )
    : Object.entries(source)
        .map(([id, config]) => normaliseMcpServer(id, config))
        .filter((server): server is StoredMcpServer => Boolean(server));
  if (!servers.length) throw new Error("empty_mcp_config");
  return servers;
}

/** Plugin catalog items carry their content inline; their `plugin:` id is never a URL to fetch. */
async function registryEntryText(entry: RegistryEntry) {
  if (entry.inlinePayload !== undefined) return entry.inlinePayload;
  return entry.installRef ? fetchRemoteText(entry.installRef) : "";
}

/**
 * Full plain-text description of connectors for the confirmation shown before
 * saving third-party (plugin catalog) configuration: command line or URL,
 * environment variables and headers, nothing hidden.
 */
export function describeMcpServersForReview(servers: readonly StoredMcpServer[]) {
  return servers
    .map((server) => {
      const lines = [
        server.id,
        server.transport === "http"
          ? `  ${server.url}`
          : `  ${[server.command, server.args].filter(Boolean).join(" ")}`,
      ];
      for (const [key, value] of Object.entries(server.env)) lines.push(`  env ${key}=${value}`);
      for (const [key, value] of Object.entries(server.headers)) lines.push(`  header ${key}: ${value}`);
      return lines.join("\n");
    })
    .join("\n\n");
}

export async function loadMcpRegistryEntry(entry: RegistryEntry) {
  const text = await registryEntryText(entry);
  if (!text) throw new Error("missing_mcp_install_source");
  const servers = parseMcpConfig(text);
  return servers.map((server, index) => ({
    ...server,
    id: index === 0 ? server.id : `${server.id}-${index + 1}`,
    docsUrl: server.docsUrl || normalizeDocumentationUrl(entry.docsUrl),
  }));
}

/** Parses a SKILL.md front-matter block and retains the full markdown as AI context. */
