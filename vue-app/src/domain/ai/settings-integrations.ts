import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";
import { normalizeTencentSkillHubDocsUrl } from "@/domain/ai/skillhub-repository";
import {
  findMcpPathPlaceholder,
  GIT_MCP_PACKAGE,
  normalizeMcpAuth,
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

/** A skill is stored as text only. Its scripts are never automatically executed. */
export interface StoredAiSkill {
  id: string;
  name: string;
  content: string;
  sourcePath?: string;
  description?: string;
  docsUrl?: string;
  enabled: boolean;
  /** "manual" for a skill written in MinCode; absent for imported and registry skills. */
  origin?: "manual";
}

export type AiSkillSource = "manual" | "local" | "registry";

export interface RegistryEntry {
  id: string;
  name: string;
  description: string;
  source: "builtin" | "git" | "registry" | "plugin";
  installRef?: string;
  docsUrl?: string;
  inlinePayload?: string;
  /** Set for items of a plugin catalog (contributes.aiCatalogs). */
  plugin?: { pluginId: string; pluginName: string; catalogId: string; itemId: string; kind?: "mcp" | "skill"; fingerprint?: string };
}

/** Supplies plugin catalog items for a Discover query; failures are the supplier's to absorb. */
export type PluginRegistrySearch = (query: string) => Promise<RegistryEntry[]>;

const noPluginEntries: PluginRegistrySearch = async () => [];

interface HttpResponse {
  status: number;
  body: string;
}

// Discover lists the built-in catalog plus plugin catalogs (contributes.aiCatalogs).
// Online sources (registries, SkillHub, Git repositories) live in the official
// connector & skill market plugin (`ai-market`), which also handles upgrades.
const MAX_SKILL_BYTES = 512 * 1024;

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

const BUILTIN_SKILL_CATALOG: RegistryEntry[] = [
  {
    id: "builtin-skill-code-review",
    name: "代码审查助手",
    description: "系统化检查正确性、安全性、性能、可读性和测试。",
    source: "builtin",
    inlinePayload: [
      "---",
      "name: 代码审查助手",
      "description: 系统化审查代码变更并输出结构化清单。",
      "---",
      "",
      "# 代码审查助手",
      "",
      "审查代码时，逐项检查正确性、安全性、性能、可读性与测试。",
      "输出时按「问题 → 位置 → 建议」分组；只报告有依据的问题，不臆测。",
    ].join("\n"),
  },
  {
    id: "builtin-skill-commit-message",
    name: "提交信息规范",
    description: "根据实际变更生成准确、简洁的 Conventional Commit。",
    source: "builtin",
    inlinePayload: [
      "---",
      "name: 提交信息规范",
      "description: 按 Conventional Commits 生成提交信息。",
      "---",
      "",
      "# 提交信息规范",
      "",
      "格式为 `<type>(<scope>): <subject>`；subject 使用祈使句、不加句号并控制在 72 个字符内。",
      "只描述实际变更；多个逻辑点使用正文要点说明。",
    ].join("\n"),
  },
  {
    id: "builtin-skill-debugging",
    name: "系统化调试",
    description: "用复现、观测、假设和验证定位根因。",
    source: "builtin",
    inlinePayload: [
      "---",
      "name: 系统化调试",
      "description: 用假设-验证法定位问题根因。",
      "---",
      "",
      "# 系统化调试",
      "",
      "遵循复现 → 观测 → 假设 → 验证 → 修复的流程。",
      "以日志、报错栈和最小实验为证据，优先修复根因并补充回归验证。",
    ].join("\n"),
  },
];

function asString(value: unknown) {
  return typeof value === "string" ? value.trim() : "";
}

/** Keeps documentation links inert unless they use a regular web protocol. */
export function normalizeDocumentationUrl(value: unknown) {
  try {
    const url = new URL(asString(value));
    if (url.protocol !== "https:" && url.protocol !== "http:") return "";
    return normalizeTencentSkillHubDocsUrl(url.toString());
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

function directUrlEntry(value: string, extension: ".json" | ".md"): RegistryEntry | null {
  const installRef = toRawContentUrl(value);
  if (!installRef || !installRef.toLowerCase().split(/[?#]/)[0]!.endsWith(extension)) return null;
  return {
    id: `git-${installRef}`,
    name: filenameWithoutExtension(installRef, extension === ".md" ? "SKILL" : "MCP Server"),
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

export async function searchMcpRegistry(query: string, searchPlugins = noPluginEntries) {
  const direct = directUrlEntry(query, ".json");
  const plugin = await searchPlugins(query).catch(() => []);
  return mergeEntries(
    direct ? [direct] : [],
    BUILTIN_MCP_CATALOG.filter((entry) => matches(entry, query)),
    plugin
  );
}

export async function searchSkillRegistry(query: string, searchPlugins = noPluginEntries) {
  const direct = directUrlEntry(query, ".md");
  const plugin = await searchPlugins(query).catch(() => []);
  if (direct) return mergeEntries([direct], plugin);
  return mergeEntries(BUILTIN_SKILL_CATALOG.filter((entry) => matches(entry, query)), plugin);
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
    ...withAuthMarker(raw.auth),
  };
}

function withAuthMarker(value: unknown): Pick<StoredMcpServer, "auth"> {
  const auth = normalizeMcpAuth(value);
  return auth ? { auth } : {};
}

/**
 * A connector read from saved settings: documentation link made inert unless
 * it is a web URL, env/headers defaulted, and only a known auth marker kept.
 */
export function normalizeStoredMcpServer(server: StoredMcpServer): StoredMcpServer {
  const { auth, ...rest } = (server ?? {}) as Partial<StoredMcpServer>;
  return {
    ...rest,
    docsUrl: normalizeDocumentationUrl(rest.docsUrl),
    env: rest.env && typeof rest.env === "object" ? rest.env : {},
    headers: rest.headers && typeof rest.headers === "object" ? rest.headers : {},
    ...withAuthMarker(auth),
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
      if (server.auth) config.auth = server.auth;
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
    if (config.auth !== undefined && !normalizeMcpAuth(config.auth)) throw new Error(`invalid_mcp_auth:${id}`);
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
      ...withAuthMarker(config.auth),
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
  if (entry.source === "plugin") throw new Error("missing_plugin_install_content");
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
export function parseAiSkillMarkdown(
  content: string,
  sourcePath: string,
  fallbackName = "SKILL"
): StoredAiSkill {
  if (new TextEncoder().encode(content).length > MAX_SKILL_BYTES)
    throw new Error("skill_too_large");
  const trimmed = content.trim();
  if (!trimmed) throw new Error("empty_skill");
  const frontMatter = trimmed.match(/^---\s*\n([\s\S]*?)\n---\s*(?:\n|$)/);
  const frontMatterMap = Object.fromEntries(
    (frontMatter?.[1] ?? "")
      .split(/\r?\n/)
      .map((line) => line.match(/^([\w-]+):\s*(.+)$/))
      .filter((match): match is RegExpMatchArray => Boolean(match))
      .map((match) => [match[1]!, match[2]!.replace(/^['"]|['"]$/g, "").trim()])
  );
  const name = asString(frontMatterMap.name) || filenameWithoutExtension(sourcePath, fallbackName);
  return {
    id: slugify(`${name}-${sourcePath}`, `skill-${Date.now()}`),
    name,
    content: trimmed,
    sourcePath,
    description: asString(frontMatterMap.description),
    docsUrl: normalizeDocumentationUrl(
      frontMatterMap.documentation ||
        frontMatterMap.docs ||
        frontMatterMap.homepage ||
        frontMatterMap.repository
    ),
    enabled: true,
  };
}

/**
 * Builds a skill written in MinCode from SKILL.md text. Unlike an imported
 * file there is no filename to fall back on, so front matter `name` is required.
 */
export function createManualAiSkill(content: string, id: string): StoredAiSkill {
  const parsed = parseAiSkillMarkdown(content, "", "");
  if (!parsed.name) throw new Error("skill_name_required");
  return {
    id,
    name: parsed.name,
    content: parsed.content,
    description: parsed.description,
    docsUrl: parsed.docsUrl,
    enabled: true,
    origin: "manual",
  };
}

/**
 * Registry installs keep their download URL, `builtin:<id>` or
 * `plugin:<pluginId>/<catalogId>/<itemId>` as source; imports keep a file path.
 */
export function aiSkillSource(skill: Pick<StoredAiSkill, "origin" | "sourcePath">): AiSkillSource {
  if (skill.origin === "manual") return "manual";
  const source = skill.sourcePath?.trim() ?? "";
  return source && !/^(?:https?:\/\/|builtin:|plugin:)/i.test(source) ? "local" : "registry";
}

/** `/skill <name>` matches names case-insensitively, so names must be unique that way. */
export function hasAiSkillNamed(skills: readonly StoredAiSkill[], name: string, exceptId?: string) {
  const key = name.trim().toLowerCase();
  return skills.some((skill) => skill.id !== exceptId && skill.name.trim().toLowerCase() === key);
}

export async function loadSkillRegistryEntry(entry: RegistryEntry) {
  const content = await registryEntryText(entry);
  if (!content) throw new Error("missing_skill_install_source");
  const skill = parseAiSkillMarkdown(content, entry.installRef ?? `builtin:${entry.id}`, entry.name);
  return {
    ...skill,
    docsUrl: skill.docsUrl || normalizeDocumentationUrl(entry.docsUrl),
  };
}
