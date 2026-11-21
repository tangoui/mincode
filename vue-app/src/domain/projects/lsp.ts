// 必须与后端 backend/lsp/registry.go 的 serverDefinitions 保持一致：
// sanitizeLspSettings 会把不在此列表里的 id 静默丢掉，漏一个的表现是
// “点了启用没反应”（写进去又被过滤掉）。
export const KNOWN_LSP_SERVER_IDS = [
  "intelephense",
  "gopls",
  "ts-ls",
  "pyright",
  "rust-analyzer",
  "json-ls",
  "yaml-ls",
  "html-ls",
  "vue-ls",
  "vue-ts",
  "css-ls",
  "bash-ls",
  "jdtls",
  "clangd",
  "lua-ls",
  "csharp-ls",
  "sqls",
  "r-languageserver",
  "ruby-lsp",
  "sourcekit-lsp",
  "perlnavigator",
  "marksman",
  "dockerfile-ls",
  "taplo",
  "graphql-lsp",
  "terraform-ls",
  "kotlin-ls",
  "svelte-ls",
  "prisma-ls",
  "dart-ls",
  "elixir-ls",
  "metals",
  "fsautocomplete",
  "zls",
  "haskell-ls",
  "ocamllsp",
  "julia-ls",
  "astro-ls",
  "protobuf-ls",
  "tailwind-ls",
  "angular-ls",
] as const;

export type LspServerId = (typeof KNOWN_LSP_SERVER_IDS)[number];

export interface LspServerInfo {
  id: string;
  name: string;
  languages: string[];
  installed: boolean;
  executable?: string;
  heavy: boolean;
  /** 辅助型服务器（如 Tailwind/Angular）：与主服务器共享语言，多会话共存。 */
  auxiliary?: boolean;
  installHint: string;
  installable: boolean;
  platform: string;
  running: boolean;
  generation?: number;
  error?: string;
  prerequisiteMissing?: boolean;
  prerequisiteHint?: string;
  /** Servers that must run beside this one (Vue 3.x hybrid: vue-ls → vue-ts); enabled implicitly. */
  companions?: string[];
}

export interface LspProjectSettings {
  /** Built-in server ids and plugin server ids (`plugin:<pluginId>/<serverId>`). */
  enabledServers: string[];
  /**
   * Language id → plugin server id. A built-in server serves its languages by
   * default; a plugin server replaces it for one language only through this
   * explicit, per-project override.
   */
  pluginLanguageOverrides?: Record<string, string>;
}

export const DEFAULT_LSP_SETTINGS: LspProjectSettings = {
  enabledServers: [],
};

/** Session id of a plugin language server (mirrors backend/lsp/external.go). */
export const PLUGIN_LSP_SERVER_ID_PATTERN = /^plugin:[a-z0-9][a-z0-9._-]{1,127}\/[a-z0-9][a-z0-9-]{0,31}$/;
/** Editor language id syntax (contributes.languages / languageServers). */
export const LSP_LANGUAGE_ID_PATTERN = /^[a-z][a-z0-9._-]{0,31}$/;
const MAX_ENABLED_SERVERS = 128;
const MAX_LANGUAGE_OVERRIDES = 64;
/** A plugin server's workspace/applyEdit is honoured only this long after a user action on its session. */
export const PLUGIN_APPLY_EDIT_WINDOW_MS = 15_000;
/**
 * Backend event (mirrors backend/lsp/watch.go fileWatchLimitedMethod): a server's
 * file-watch registration was acknowledged without watching because the project
 * exceeds the watch limits, so external file changes do not reach that server.
 */
export const LSP_FILE_WATCH_LIMITED_METHOD = "$/mincode/fileWatchLimited";
/**
 * Backend event (mirrors backend/lsp/watch.go fileWatchScanMethod) bracketing the
 * baseline scan of a file-watch registration: `{ state: "begin" | "end" }`.
 */
export const LSP_FILE_WATCH_SCAN_METHOD = "$/mincode/fileWatchScan";

export function isPluginLspServerId(id: string): boolean {
  return PLUGIN_LSP_SERVER_ID_PATTERN.test(id);
}

/** Splits `plugin:<pluginId>/<serverId>`; undefined for anything else. */
export function parsePluginLspServerId(id: string): { pluginId: string; serverId: string } | undefined {
  if (!isPluginLspServerId(id)) return undefined;
  const rest = id.slice("plugin:".length);
  const slash = rest.lastIndexOf("/");
  return { pluginId: rest.slice(0, slash), serverId: rest.slice(slash + 1) };
}

export function pluginLspServerId(pluginId: string, serverId: string): string {
  return `plugin:${pluginId}/${serverId}`;
}

export function sanitizeLspSettings(value: unknown): LspProjectSettings {
  if (!value || typeof value !== "object") return { ...DEFAULT_LSP_SETTINGS };
  const raw = value as Record<string, unknown>;
  const enabledServers = Array.isArray(raw.enabledServers)
    ? [
        ...new Set(
          raw.enabledServers.filter(
            (id): id is string =>
              typeof id === "string" &&
              ((KNOWN_LSP_SERVER_IDS as readonly string[]).includes(id) || isPluginLspServerId(id)),
          ),
        ),
      ].slice(0, MAX_ENABLED_SERVERS)
    : [];
  const settings: LspProjectSettings = { enabledServers };
  const overrides = raw.pluginLanguageOverrides;
  if (overrides && typeof overrides === "object" && !Array.isArray(overrides)) {
    const kept: Record<string, string> = {};
    let count = 0;
    for (const [language, serverId] of Object.entries(overrides as Record<string, unknown>)) {
      if (count >= MAX_LANGUAGE_OVERRIDES) break;
      if (!LSP_LANGUAGE_ID_PATTERN.test(language) || typeof serverId !== "string" || !isPluginLspServerId(serverId)) continue;
      kept[language] = serverId;
      count += 1;
    }
    // Overrides of missing plugins stay inert (the built-in keeps serving).
    if (count) settings.pluginLanguageOverrides = kept;
  }
  return settings;
}

export interface LanguageRoutingContext {
  /** Enabled server ids (built-in and plugin). */
  enabled: ReadonlySet<string>;
  overrides: Readonly<Record<string, string>>;
  /** Primary (non-auxiliary) built-in server id → languages. */
  builtinLanguages: ReadonlyMap<string, readonly string[]>;
  /** Plugin server id → declared languages (active plugins only). */
  pluginLanguages: ReadonlyMap<string, readonly string[]>;
}

export type LanguageRoute = { kind: "builtin" } | { kind: "plugin"; serverId: string };

/**
 * Decides who serves `language` in a project. A built-in server wins for any
 * language an enabled built-in covers, unless the project override names an
 * enabled plugin server that declares the language. Otherwise the first
 * enabled plugin server (ordered by id, so by plugin id) serves it.
 */
export function resolveLanguageRouting(language: string, context: LanguageRoutingContext): LanguageRoute | undefined {
  const override = context.overrides[language];
  if (
    override !== undefined &&
    Object.prototype.hasOwnProperty.call(context.overrides, language) &&
    context.enabled.has(override) &&
    context.pluginLanguages.get(override)?.includes(language)
  ) {
    return { kind: "plugin", serverId: override };
  }
  for (const [serverId, languages] of context.builtinLanguages) {
    if (context.enabled.has(serverId) && languages.includes(language)) return { kind: "builtin" };
  }
  const plugins = [...context.pluginLanguages.keys()].sort();
  for (const serverId of plugins) {
    if (context.enabled.has(serverId) && context.pluginLanguages.get(serverId)?.includes(language)) {
      return { kind: "plugin", serverId };
    }
  }
  return undefined;
}

/**
 * The languages `serverId` actually serves: a plugin server only the
 * languages routed to it; a primary built-in every declared language not
 * overridden to a plugin server. Auxiliary built-ins keep all of theirs.
 */
export function routedServerLanguages(
  serverId: string,
  declared: readonly string[],
  context: LanguageRoutingContext,
  auxiliary = false,
): string[] {
  if (isPluginLspServerId(serverId)) {
    return declared.filter((language) => {
      const route = resolveLanguageRouting(language, context);
      return route?.kind === "plugin" && route.serverId === serverId;
    });
  }
  if (auxiliary) return [...declared];
  return declared.filter((language) => resolveLanguageRouting(language, context)?.kind !== "plugin");
}

/** Whether a plugin server's unsolicited workspace/applyEdit may be applied. */
export function pluginApplyEditAllowed(lastUserActionAt: number | undefined, now: number): boolean {
  return lastUserActionAt !== undefined && now >= lastUserActionAt && now - lastUserActionAt <= PLUGIN_APPLY_EDIT_WINDOW_MS;
}

/**
 * The textDocument languageId sent to language servers. Monaco highlights
 * Terraform as "hcl", but terraform-ls only serves "terraform" and
 * "terraform-vars" and rejects every request for other IDs.
 */
export function lspDocumentLanguageId(languageId: string, path: string): string {
  if (languageId === "hcl") {
    const lower = path.toLowerCase();
    if (lower.endsWith(".tf")) return "terraform";
    if (lower.endsWith(".tfvars")) return "terraform-vars";
  }
  return languageId;
}
