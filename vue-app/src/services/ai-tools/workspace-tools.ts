import type { AiCapability } from "@/domain/ai/capability-registry";
import type { WorkspaceView } from "@/types/workspace";
import {
  optionalRelativePath,
  relativePath,
  requiredString,
  resourceCapability,
  resourceMetadataPath,
  sanitizeAiResult,
  stringArgument,
  tool,
  type AiToolDependencies,
} from "./shared";

export const workspaceCapabilityNames = [
  "workspace.open_view",
  "workspace.list_saved_projects",
  "workspace.select_saved_project",
  "workspace.open_project_file",
  "workspace.save_project_file",
  "workspace.close_project_file",
  "workspace.refresh_project",
  "workspace.reveal_project",
  "workspace.rename_saved_project",
  "workspace.remove_saved_project",
  "workspace.describe_current_project",
  "workspace.list_files",
  "workspace.read_file",
  "workspace.search",
  "workspace.search_code",
  "workspace.write_file",
  "workspace.replace_preview",
  "workspace.replace_apply",
  "workspace.run_command",
  "workspace.create_directory",
  "workspace.rename_entry",
  "workspace.delete_entry",
  "workspace.copy_entry",
  "workspace.move_entry",
  "workspace.stat",
  "workspace.open_worktree_inspector",
  "workspace.select_worktree_repo",
  "workspace.bind_worktree",
  "workspace.unbind_worktree",
  "workspace.refresh_file_index",
  "workspace.search_file_index",
  "workspace.regex_search",
  "workspace.list_all_files",
  "workspace.is_binary",
  "workspace.reveal_entry",
  "workspace.list_language_servers",
  "workspace.start_language_server",
  "workspace.stop_language_server",
  "workspace.language_server_logs",
  "workspace.install_language_server",
  "workspace.language_server_query",
  "workspace.language_server_notify",
];

const AI_OPENABLE_WORKSPACE_VIEWS = [
  "dashboard",
  "terminal",
  "ssh",
  "projects",
  "database",
  "sftp",
  "http",
  "sessions",
  "browser",
  "plugins",
] as const satisfies readonly WorkspaceView[];

type AiOpenableWorkspaceView = (typeof AI_OPENABLE_WORKSPACE_VIEWS)[number];

export interface WorkspaceAiDependencies {
  openWorkspaceView?(input: {
    view: AiOpenableWorkspaceView;
  }): Promise<unknown>;
  projectAction?(input: Record<string, unknown>): Promise<unknown>;
  listSavedProjects?(conversationId?: string): Array<{ root: string; name: string }>;
}

const CODE_SEARCH_MODES = ["auto", "path", "text", "symbol"] as const;
type CodeSearchMode = (typeof CODE_SEARCH_MODES)[number];

interface WorkspaceCodeSearchMatch {
  relativePath: string;
  kind: "path" | "text" | "symbol";
  line?: number;
  column?: number;
  lineText?: string;
  score: number;
  reason: string;
}

interface WorkspaceCodeSearchResult {
  query: string;
  mode: CodeSearchMode;
  engine: string;
  filesConsidered: number;
  results: WorkspaceCodeSearchMatch[];
  truncated: boolean;
  durationMs: number;
}

const sensitiveFileNames = new Set([
  ".env",
  ".npmrc",
  ".pypirc",
  ".netrc",
  "id_rsa",
  "id_ed25519",
  "credentials",
]);

export function safeWorkspacePath(
  input: Record<string, unknown>,
  key: string,
  capability: string,
) {
  const path = relativePath(input, key, capability);
  const segments = path.toLowerCase().split("/");
  const name = segments[segments.length - 1] ?? "";
  if (
    segments.some(
      (segment) =>
        sensitiveFileNames.has(segment) || segment.startsWith(".env."),
    ) ||
    /\.(pem|key|p12|pfx|kdbx)$/i.test(name)
  ) {
    throw new Error(`${capability} cannot access sensitive files`);
  }
  return path;
}

function replaceArguments(input: Record<string, unknown>, capability: string) {
  return {
    pattern: requiredString(input, "pattern", capability),
    replacement: typeof input.replacement === "string" ? input.replacement : "",
    caseSensitive: input.caseSensitive === true,
    wholeWord: input.wholeWord === true,
  };
}

function isMissingWorkspacePathError(error: unknown) {
  const message = (
    error instanceof Error ? error.message : String(error)
  ).toLowerCase();
  return (
    message.includes("no such file") ||
    message.includes("not exist") ||
    message.includes("cannot find the file")
  );
}

function safeWorkspaceResults(
  value: unknown,
): Array<Record<string, unknown> & { relativePath: string }> {
  if (!Array.isArray(value)) return [];
  return value
    .map((entry) => {
      if (!entry || typeof entry !== "object") return null;
      const record = entry as Record<string, unknown>;
      const candidate =
        typeof record.relativePath === "string"
          ? record.relativePath
          : typeof record.path === "string"
            ? record.path
            : "";
      if (!candidate) return null;
      try {
        const path = safeWorkspacePath(
          { path: candidate },
          "path",
          "workspace.result",
        );
        const { path: _absolutePath, ...safe } = record;
        return { ...safe, relativePath: path };
      } catch {
        return null;
      }
    })
    .filter(
      (entry): entry is Record<string, unknown> & { relativePath: string } =>
        entry !== null,
    );
}

const LSP_QUERY_METHODS = [
  "textDocument/definition",
  "textDocument/typeDefinition",
  "textDocument/implementation",
  "textDocument/references",
  "textDocument/hover",
  "textDocument/documentSymbol",
  "textDocument/completion",
  "textDocument/signatureHelp",
  "textDocument/diagnostic",
  "workspace/symbol",
] as const;

const LSP_SYNC_METHODS = [
  "textDocument/didOpen",
  "textDocument/didChange",
  "textDocument/didSave",
  "textDocument/didClose",
] as const;

const serverId = {
  type: "string",
  description: "Server id from workspace.list_language_servers.",
};

// Plugin language servers (`plugin:<pluginId>/<id>`) are user-managed: the
// user enables and confirms them in the project dialog. AI tools can neither
// list, start, stop nor drive them (the host also refuses project_lsp_start).
function builtinLanguageServerId(input: Record<string, unknown>, toolName: string) {
  const id = requiredString(input, "serverId", toolName);
  if (id.trim().toLowerCase().startsWith("plugin:")) {
    throw new Error(`${toolName}: plugin language servers are user-managed and not available to AI tools`);
  }
  return id;
}

function withoutPluginLanguageServers(servers: unknown) {
  if (!Array.isArray(servers)) return servers;
  return servers.filter(
    (server) =>
      !(typeof server === "object" && server !== null && String((server as { id?: unknown }).id ?? "").startsWith("plugin:")),
  );
}

function lspGeneration(input: Record<string, unknown>) {
  const value = input.generation;
  if (typeof value !== "number" || !Number.isInteger(value) || value <= 0) {
    throw new Error(
      "a language server generation id is required; call workspace.start_language_server first",
    );
  }
  return value;
}

function lspMethod(
  input: Record<string, unknown>,
  allowed: readonly string[],
  capability: string,
) {
  const method = requiredString(input, "method", capability);
  if (!allowed.includes(method)) {
    throw new Error(`${capability} does not support method: ${method}`);
  }
  return method;
}

/**
 * LSP params 里的 uri 是模型自己填的，后端只按 root 找会话、不校验文档路径。
 * 不挡的话一个 file:///Users/x/.ssh/id_rsa 就能让语言服务器替它读项目外的文件。
 */
function assertLspUrisAreSafe(
  value: unknown,
  projectRoot: string,
  seen = new WeakSet<object>(),
): void {
  if (!value || typeof value !== "object") return;
  if (seen.has(value)) return;
  seen.add(value);
  if (Array.isArray(value)) {
    for (const entry of value) assertLspUrisAreSafe(entry, projectRoot, seen);
    return;
  }
  for (const [key, entry] of Object.entries(value as Record<string, unknown>)) {
    if (typeof entry === "string" && /uri$/i.test(key)) {
      assertLspUriIsSafe(entry, projectRoot);
      continue;
    }
    assertLspUrisAreSafe(entry, projectRoot, seen);
  }
}

function assertLspUriIsSafe(uri: string, projectRoot: string) {
  if (!uri.startsWith("file://")) {
    throw new Error(
      "a language server uri must be a file:// uri inside the bound project",
    );
  }
  let decoded: string;
  try {
    decoded = decodeURIComponent(
      uri.slice("file://".length).replace(/^\/\/[^/]*/, ""),
    );
  } catch {
    throw new Error("a language server uri must be a valid file:// uri");
  }
  const root = projectRoot.replace(/\/+$/, "");
  if (decoded !== root && !decoded.startsWith(`${root}/`)) {
    throw new Error("a language server uri must stay inside the bound project");
  }
  const relative = decoded.slice(root.length + 1);
  if (relative)
    safeWorkspacePath({ path: relative }, "path", "workspace.language_server");
}

function lspParams(input: Record<string, unknown>, projectRoot: string) {
  const params =
    input.params &&
    typeof input.params === "object" &&
    !Array.isArray(input.params)
      ? (input.params as Record<string, unknown>)
      : {};
  assertLspUrisAreSafe(params, projectRoot);
  return params;
}

function codeSearchMode(input: Record<string, unknown>): CodeSearchMode {
  const mode = stringArgument(input, "mode") || "auto";
  if (!CODE_SEARCH_MODES.includes(mode as CodeSearchMode)) {
    throw new Error(`workspace.search_code does not support mode: ${mode}`);
  }
  return mode as CodeSearchMode;
}

function codeSearchLimit(input: Record<string, unknown>) {
  if (typeof input.limit !== "number" || !Number.isFinite(input.limit))
    return 40;
  return Math.max(1, Math.min(100, Math.round(input.limit)));
}

function safeWorkspaceCodeSearchResult(
  value: unknown,
): WorkspaceCodeSearchResult {
  const record =
    value && typeof value === "object" && !Array.isArray(value)
      ? (value as Record<string, unknown>)
      : {};
  const mode = CODE_SEARCH_MODES.includes(record.mode as CodeSearchMode)
    ? (record.mode as CodeSearchMode)
    : "auto";
  const results = safeWorkspaceResults(record.results).flatMap((entry) => {
    const kind = entry.kind;
    if (kind !== "path" && kind !== "text" && kind !== "symbol") return [];
    const relativePath =
      typeof entry.relativePath === "string" ? entry.relativePath : "";
    const score =
      typeof entry.score === "number" && Number.isFinite(entry.score)
        ? Math.round(entry.score)
        : 0;
    const line =
      typeof entry.line === "number" &&
      Number.isInteger(entry.line) &&
      entry.line > 0
        ? entry.line
        : undefined;
    const column =
      typeof entry.column === "number" &&
      Number.isInteger(entry.column) &&
      entry.column > 0
        ? entry.column
        : undefined;
    const lineText =
      typeof entry.lineText === "string"
        ? entry.lineText.slice(0, 500)
        : undefined;
    const reason =
      typeof entry.reason === "string"
        ? entry.reason.slice(0, 160)
        : "code search match";
    return [
      {
        relativePath,
        kind,
        line,
        column,
        lineText,
        score,
        reason,
      } satisfies WorkspaceCodeSearchMatch,
    ];
  });
  return sanitizeAiResult({
    query: typeof record.query === "string" ? record.query.slice(0, 256) : "",
    mode,
    engine:
      typeof record.engine === "string"
        ? record.engine.slice(0, 64)
        : "unknown",
    filesConsidered:
      typeof record.filesConsidered === "number" && record.filesConsidered >= 0
        ? Math.round(record.filesConsidered)
        : 0,
    results,
    truncated: record.truncated === true,
    durationMs:
      typeof record.durationMs === "number" && record.durationMs >= 0
        ? Math.round(record.durationMs)
        : 0,
  }) as WorkspaceCodeSearchResult;
}

/** 索引是跨项目的，不限定 roots 就会扫到用户没保存的目录，所以只放行已保存项目。 */
function savedProjectRoots(
  input: Record<string, unknown>,
  dependencies: WorkspaceAiDependencies,
  capability: string,
  conversationId?: string,
) {
  const saved =
    dependencies.listSavedProjects?.(conversationId).map(({ root }) => root) ?? [];
  if (!saved.length) throw new Error("会话工作区没有可访问的本地项目");
  if (input.roots === undefined) return saved;
  if (
    !Array.isArray(input.roots) ||
    input.roots.some((root) => typeof root !== "string")
  ) {
    throw new Error(`${capability} requires roots to be strings`);
  }
  const requested = input.roots.map((root) => String(root).trim());
  if (!requested.length) throw new Error("索引查询必须指定工作区内的项目");
  const unknown = requested.filter((root) => !saved.includes(root));
  if (unknown.length) {
    throw new Error(
      `${capability} can only reach saved projects, not: ${unknown.join(", ")}`,
    );
  }
  return requested;
}

export function workspaceCapabilities(
  dependencies: AiToolDependencies & WorkspaceAiDependencies,
): AiCapability[] {
  const workspace = resourceCapability;
  return [
    ...(dependencies.openWorkspaceView
      ? ([
          {
            module: "workspace",
            discovery: true,
            target: "none" as const,
            tool: tool(
              "workspace.open_view",
              "Open and focus a MinCode workspace page or tab. Use 'projects' for the left-side Local Projects screen and 'dashboard' for the home page.",
              {
                view: {
                  type: "string",
                  enum: AI_OPENABLE_WORKSPACE_VIEWS,
                  description:
                    "The workspace tab to open. Use 'projects' for Local Projects.",
                },
              },
              ["view"],
            ),
            mode: "read" as const,
            risk: "read" as const,
            execute: ({ input }: { input: Record<string, unknown> }) => {
              const view = requiredString(input, "view", "workspace.open_view");
              if (
                !AI_OPENABLE_WORKSPACE_VIEWS.includes(
                  view as AiOpenableWorkspaceView,
                )
              ) {
                throw new Error(
                  `workspace.open_view does not support view: ${view}`,
                );
              }
              return dependencies.openWorkspaceView!({
                view: view as AiOpenableWorkspaceView,
              });
            },
          },
        ] satisfies AiCapability[])
      : []),
    ...(dependencies.listSavedProjects
      ? ([
          {
            module: "workspace",
            target: "none" as const,
            discovery: true,
            tool: tool(
              "workspace.list_saved_projects",
              "List Local Projects saved in the desktop project workspace.",
            ),
            mode: "read" as const,
            risk: "read" as const,
            execute: ({ conversationId }) =>
              dependencies.listSavedProjects!(conversationId).map(({ root, name }) => ({
                root,
                name,
              })),
          },
          {
            module: "workspace",
            target: "none" as const,
            tool: tool(
              "workspace.refresh_file_index",
              "Rescan saved Local Projects into the cross-project file index. Run this before searching if files changed outside MinCode. Defaults to every saved project.",
              { roots: { type: "array", items: { type: "string" } } },
            ),
            // Rebuilds MinCode's metadata-only cache without changing project files.
            // Keep mode aligned with read risk; the registry rejects write/read pairs at mount time.
            mode: "read" as const,
            risk: "read" as const,
            execute: ({ input, conversationId }) =>
              dependencies.invoke("project_index_refresh", {
                roots: savedProjectRoots(
                  input,
                  dependencies,
                  "workspace.refresh_file_index",
                  conversationId,
                ),
              }),
          },
          {
            module: "workspace",
            target: "none" as const,
            tool: tool(
              "workspace.search_file_index",
              "Search file paths across several saved Local Projects at once. Unlike workspace.search this is not bound to one project, and it matches names and paths only, never file contents.",
              {
                query: { type: "string" },
                roots: { type: "array", items: { type: "string" } },
                limit: { type: "integer", minimum: 1, maximum: 200 },
              },
              ["query"],
            ),
            mode: "read" as const,
            risk: "read" as const,
            execute: ({ input, conversationId }) =>
              dependencies.invoke("project_index_search", {
                roots: savedProjectRoots(
                  input,
                  dependencies,
                  "workspace.search_file_index",
                  conversationId,
                ),
                query: requiredString(
                  input,
                  "query",
                  "workspace.search_file_index",
                ),
                limit:
                  typeof input.limit === "number"
                    ? Math.max(1, Math.min(200, Math.round(input.limit)))
                    : 80,
              }),
          },
        ] satisfies AiCapability[])
      : []),
    ...(dependencies.projectAction
      ? ([
          {
            module: "workspace",
            target: "none" as const,
            tool: tool(
              "workspace.select_saved_project",
              "Switch the visible Local Projects workspace to one already-saved project.",
              { root: { type: "string" } },
              ["root"],
            ),
            mode: "write" as const,
            risk: "write" as const,
            approvalPreview: ({ input }: { input: Record<string, unknown> }) =>
              requiredString(input, "root", "workspace.select_saved_project"),
            execute: ({ input }: { input: Record<string, unknown> }) =>
              dependencies.projectAction!({
                action: "select",
                root: requiredString(
                  input,
                  "root",
                  "workspace.select_saved_project",
                ),
              }),
          },
          {
            module: "workspace",
            target: "none" as const,
            tool: tool(
              "workspace.open_project_file",
              "Open and focus a project-relative file in the visible Monaco editor. Optionally reveal a 1-based line.",
              {
                path: { type: "string" },
                projectRoot: { type: "string" },
                line: { type: "integer", minimum: 1 },
              },
              ["path"],
            ),
            mode: "write" as const,
            risk: "write" as const,
            approvalPreview: ({ input }: { input: Record<string, unknown> }) =>
              safeWorkspacePath(input, "path", "workspace.open_project_file"),
            execute: ({ input }: { input: Record<string, unknown> }) =>
              dependencies.projectAction!({
                action: "open-file",
                path: safeWorkspacePath(
                  input,
                  "path",
                  "workspace.open_project_file",
                ),
                projectRoot: stringArgument(input, "projectRoot") || undefined,
                line:
                  typeof input.line === "number"
                    ? Math.max(1, Math.floor(input.line))
                    : undefined,
              }),
          },
          {
            module: "workspace",
            target: "none" as const,
            tool: tool(
              "workspace.refresh_project",
              "Refresh the visible Local Projects file tree.",
            ),
            mode: "read" as const,
            risk: "read" as const,
            execute: () => dependencies.projectAction!({ action: "refresh" }),
          },
          {
            module: "workspace",
            target: "none" as const,
            tool: tool(
              "workspace.save_project_file",
              "Save one already-open edited project file in the visible Monaco editor.",
              { path: { type: "string" } },
              ["path"],
            ),
            mode: "write" as const,
            risk: "write" as const,
            approvalPreview: ({ input }: { input: Record<string, unknown> }) =>
              safeWorkspacePath(input, "path", "workspace.save_project_file"),
            execute: ({ input }: { input: Record<string, unknown> }) =>
              dependencies.projectAction!({
                action: "save-file",
                path: safeWorkspacePath(
                  input,
                  "path",
                  "workspace.save_project_file",
                ),
              }),
          },
          {
            module: "workspace",
            target: "none" as const,
            tool: tool(
              "workspace.close_project_file",
              "Close one already-open project editor tab. Dirty files require explicit discard confirmation in the visible UI.",
              { path: { type: "string" } },
              ["path"],
            ),
            mode: "write" as const,
            risk: "write" as const,
            approvalPreview: ({ input }: { input: Record<string, unknown> }) =>
              safeWorkspacePath(input, "path", "workspace.close_project_file"),
            execute: ({ input }: { input: Record<string, unknown> }) =>
              dependencies.projectAction!({
                action: "close-file",
                path: safeWorkspacePath(
                  input,
                  "path",
                  "workspace.close_project_file",
                ),
              }),
          },
          {
            module: "workspace",
            target: "none" as const,
            tool: tool(
              "workspace.reveal_project",
              "Show one saved local project in Finder/File Explorer.",
              { root: { type: "string" } },
              ["root"],
            ),
            mode: "write" as const,
            risk: "write" as const,
            approvalPreview: ({ input }: { input: Record<string, unknown> }) =>
              requiredString(input, "root", "workspace.reveal_project"),
            execute: ({ input }: { input: Record<string, unknown> }) =>
              dependencies.projectAction!({
                action: "reveal-project",
                root: requiredString(input, "root", "workspace.reveal_project"),
              }),
          },
          {
            module: "workspace",
            target: "none" as const,
            tool: tool(
              "workspace.rename_saved_project",
              "Rename a saved Local Project entry without changing its folder on disk.",
              { root: { type: "string" }, name: { type: "string" } },
              ["root", "name"],
            ),
            mode: "write" as const,
            risk: "write" as const,
            approvalPreview: ({ input }: { input: Record<string, unknown> }) =>
              `${requiredString(input, "root", "workspace.rename_saved_project")} -> ${requiredString(input, "name", "workspace.rename_saved_project")}`,
            execute: ({ input }: { input: Record<string, unknown> }) =>
              dependencies.projectAction!({
                action: "rename-project",
                root: requiredString(
                  input,
                  "root",
                  "workspace.rename_saved_project",
                ),
                name: requiredString(
                  input,
                  "name",
                  "workspace.rename_saved_project",
                ),
              }),
          },
          {
            module: "workspace",
            target: "none" as const,
            tool: tool(
              "workspace.remove_saved_project",
              "Remove a saved Local Project entry from MinCode. It never deletes the folder on disk.",
              { root: { type: "string" } },
              ["root"],
            ),
            mode: "write" as const,
            risk: "dangerous" as const,
            approvalPreview: ({ input }: { input: Record<string, unknown> }) =>
              `Remove saved project ${requiredString(input, "root", "workspace.remove_saved_project")} (files are not deleted)`,
            execute: ({ input }: { input: Record<string, unknown> }) =>
              dependencies.projectAction!({
                action: "remove-project",
                root: requiredString(
                  input,
                  "root",
                  "workspace.remove_saved_project",
                ),
              }),
          },
          {
            module: "workspace",
            target: "none" as const,
            tool: tool(
              "workspace.open_worktree_inspector",
              "Open the visible Git worktree drawer. Use only when the user explicitly asks to open, show, or interact with the worktree inspector; never use it merely to identify the current project or inspect Git state.",
              {},
            ),
            mode: "read" as const,
            risk: "read" as const,
            execute: () =>
              dependencies.projectAction!({
                action: "open-worktree-inspector",
              }),
          },
          {
            module: "workspace",
            target: "none" as const,
            tool: tool(
              "workspace.select_worktree_repo",
              "Change the repository selected in the visible Git worktree drawer. Use only when the user explicitly asks to interact with that drawer; this changes visible UI state and is not a project-information query.",
              { repoPath: { type: "string" } },
              ["repoPath"],
            ),
            mode: "read" as const,
            risk: "read" as const,
            execute: ({ input }: { input: Record<string, unknown> }) =>
              dependencies.projectAction!({
                action: "select-worktree-repo",
                repoPath: requiredString(
                  input,
                  "repoPath",
                  "workspace.select_worktree_repo",
                ),
              }),
          },
          {
            module: "workspace",
            target: "none" as const,
            tool: tool(
              "workspace.bind_worktree",
              "Bind the active MinCode workspace to a project folder or one of its Git worktrees, so terminals and sessions run there. Defaults to the visible project root.",
              { root: { type: "string" } },
            ),
            mode: "write" as const,
            risk: "write" as const,
            approvalPreview: ({ input }: { input: Record<string, unknown> }) =>
              `Bind the active workspace to ${stringArgument(input, "root") || "the visible project root"}`,
            execute: ({ input }: { input: Record<string, unknown> }) =>
              dependencies.projectAction!({
                action: "bind-workspace",
                root: stringArgument(input, "root") || undefined,
              }),
          },
          {
            module: "workspace",
            target: "none" as const,
            tool: tool(
              "workspace.unbind_worktree",
              "Clear the worktree binding on the active MinCode workspace. It never deletes the folder on disk.",
            ),
            mode: "write" as const,
            risk: "write" as const,
            approvalPreview: () =>
              "Clear the active workspace's worktree binding (files are not deleted)",
            execute: () =>
              dependencies.projectAction!({ action: "unbind-workspace" }),
          },
        ] satisfies AiCapability[])
      : []),
    workspace({
      module: "workspace",
      discovery: true,
      tool: tool(
        "workspace.describe_current_project",
        "Return the current MinCode workspace's bound project name and root without opening, switching, or changing any UI. Prefer this for questions such as 'what project am I in', 'current project', or '当前是什么项目'.",
      ),
      mode: "read",
      risk: "read",
      execute: ({ resource }) => {
        const projectRoot = resourceMetadataPath(resource, "projectRoot");
        const segments = projectRoot.split("/").filter(Boolean);
        return {
          name: segments[segments.length - 1] ?? "workspace",
          projectRoot,
        };
      },
    }),
    workspace({
      module: "workspace",
      discovery: true,
      tool: tool(
        "workspace.list_files",
        "List files and folders in the bound project.",
        { path: { type: "string" } },
      ),
      mode: "read",
      risk: "read",
      execute: async ({ input, resource }) => {
        const path = optionalRelativePath(
          input,
          "path",
          "workspace.list_files",
        );
        if (path) safeWorkspacePath({ path }, "path", "workspace.list_files");
        return safeWorkspaceResults(
          await dependencies.invoke("local_fs_list_dir", {
            projectRoot: resourceMetadataPath(resource, "projectRoot"),
            dirPath: path,
          }),
        );
      },
    }),
    workspace({
      module: "workspace",
      tool: tool(
        "workspace.read_file",
        "Read a non-sensitive UTF-8 file in the bound project.",
        { path: { type: "string" } },
        ["path"],
      ),
      mode: "read",
      risk: "read",
      execute: async ({ input, resource }) =>
        sanitizeAiResult(
          await dependencies.invoke("local_fs_read_text_file", {
            projectRoot: resourceMetadataPath(resource, "projectRoot"),
            filePath: safeWorkspacePath(input, "path", "workspace.read_file"),
          }),
        ),
    }),
    workspace({
      module: "workspace",
      tool: tool(
        "workspace.search",
        "Search names and text in the bound project.",
        { query: { type: "string" }, path: { type: "string" } },
        ["query"],
      ),
      mode: "read",
      risk: "read",
      execute: async ({ input, resource }) =>
        safeWorkspaceResults(
          await dependencies.invoke("local_fs_search", {
            projectRoot: resourceMetadataPath(resource, "projectRoot"),
            scopePath: stringArgument(input, "path")
              ? safeWorkspacePath(input, "path", "workspace.search")
              : null,
            query: requiredString(input, "query", "workspace.search"),
            includeContent: true,
          }),
        ),
    }),
    workspace({
      module: "workspace",
      tool: tool(
        "workspace.regex_search",
        "Search the bound project with a regular expression and get every match with its line and column. Use workspace.search_code for plain identifiers; use this when the pattern needs regex syntax.",
        {
          pattern: { type: "string", description: "Go regular expression." },
          caseSensitive: { type: "boolean" },
          wholeWord: { type: "boolean" },
          includeGlob: {
            type: "string",
            description:
              "Filename glob such as *.ts; omit to search every file.",
          },
        },
        ["pattern"],
      ),
      mode: "read",
      risk: "read",
      execute: async ({ input, resource }) =>
        safeWorkspaceResults(
          await dependencies.invoke("local_fs_regex_search", {
            projectRoot: resourceMetadataPath(resource, "projectRoot"),
            pattern: requiredString(input, "pattern", "workspace.regex_search"),
            caseSensitive: input.caseSensitive === true,
            wholeWord: input.wholeWord === true,
            includeGlob: stringArgument(input, "includeGlob") || null,
          }),
        ),
    }),
    workspace({
      module: "workspace",
      tool: tool(
        "workspace.list_all_files",
        "List every file path in the bound project as a flat list, skipping build and dependency directories. Useful for getting an overview before searching.",
      ),
      mode: "read",
      risk: "read",
      execute: async ({ resource }) =>
        safeWorkspaceResults(
          await dependencies.invoke("local_fs_list_all_files", {
            projectRoot: resourceMetadataPath(resource, "projectRoot"),
          }),
        ),
    }),
    workspace({
      module: "workspace",
      tool: tool(
        "workspace.is_binary",
        "Check whether a file in the bound project is binary, so a text read can be skipped.",
        { path: { type: "string" } },
        ["path"],
      ),
      mode: "read",
      risk: "read",
      execute: ({ input, resource }) =>
        dependencies.invoke("local_fs_peek_binary_for_preview", {
          projectRoot: resourceMetadataPath(resource, "projectRoot"),
          filePath: safeWorkspacePath(input, "path", "workspace.is_binary"),
        }),
    }),
    workspace({
      module: "workspace",
      tool: tool(
        "workspace.reveal_entry",
        "Show a file or folder of the bound project in the system file manager.",
        { path: { type: "string" } },
        ["path"],
      ),
      mode: "write",
      risk: "write",
      execute: ({ input, resource }) =>
        dependencies.invoke("local_fs_show_in_file_manager", {
          projectRoot: resourceMetadataPath(resource, "projectRoot"),
          path: safeWorkspacePath(input, "path", "workspace.reveal_entry"),
        }),
    }),
    workspace({
      module: "workspace",
      discovery: true,
      tool: tool(
        "workspace.search_code",
        "Search the bound codebase with ranked path, exact text, and symbol-declaration matches. Prefer this before reading files. Use concise identifiers or code phrases and call it repeatedly to follow related symbols.",
        {
          query: {
            type: "string",
            description:
              "A concise identifier, symbol, path fragment, or exact code phrase.",
          },
          mode: {
            type: "string",
            enum: CODE_SEARCH_MODES,
            description:
              "Use auto unless a path, exact text, or declaration-only search is required.",
          },
          path: {
            type: "string",
            description:
              "Optional project-relative directory or regular-file scope. Only provide a path confirmed by workspace.list_files or an earlier tool result; otherwise omit it to search the bound project root.",
          },
          limit: { type: "integer", minimum: 1, maximum: 100 },
        },
        ["query"],
      ),
      mode: "read",
      risk: "read",
      execute: async ({ input, resource }) => {
        const path = optionalRelativePath(
          input,
          "path",
          "workspace.search_code",
        );
        if (path) safeWorkspacePath({ path }, "path", "workspace.search_code");
        const result = await dependencies.invoke("local_fs_search_code", {
          projectRoot: resourceMetadataPath(resource, "projectRoot"),
          scopePath: path,
          query: requiredString(input, "query", "workspace.search_code"),
          mode: codeSearchMode(input),
          limit: codeSearchLimit(input),
        });
        return safeWorkspaceCodeSearchResult(result);
      },
    }),
    workspace({
      module: "workspace",
      tool: tool(
        "workspace.write_file",
        "Immediately create or replace a non-sensitive UTF-8 file in the bound project. The conversation keeps a review entry so the user can keep or revert the on-disk change.",
        { path: { type: "string" }, content: { type: "string" } },
        ["path", "content"],
      ),
      mode: "write",
      risk: "write",
      approvalPreview: ({ input }) =>
        safeWorkspacePath(input, "path", "workspace.write_file"),
      execute: async ({ input, resource }) => {
        const path = safeWorkspacePath(input, "path", "workspace.write_file");
        const projectRoot = resourceMetadataPath(resource, "projectRoot");
        const content = typeof input.content === "string" ? input.content : "";
        let before: string | null = null;
        if (dependencies.stageWorkspaceEdit) {
          try {
            before = await dependencies.invoke<string>(
              "local_fs_read_text_file",
              { projectRoot, filePath: path },
            );
          } catch (error) {
            if (!isMissingWorkspacePathError(error)) throw error;
          }
        }
        await dependencies.invoke("local_fs_write_text_file", {
          projectRoot,
          filePath: path,
          content,
        });
        if (dependencies.stageWorkspaceEdit) {
          const edit = dependencies.stageWorkspaceEdit({
            path,
            projectRoot,
            before,
            after: content,
          });
          const editId =
            edit &&
            typeof edit === "object" &&
            typeof (edit as { id?: unknown }).id === "string"
              ? (edit as { id: string }).id
              : undefined;
          return { path, written: true, reviewPending: true, editId };
        }
        return { path, written: true };
      },
    }),
    workspace({
      module: "workspace",
      tool: tool(
        "workspace.replace_preview",
        "Preview a workspace-wide regular-expression replacement without changing files.",
        {
          pattern: { type: "string" },
          replacement: { type: "string" },
          caseSensitive: { type: "boolean" },
          wholeWord: { type: "boolean" },
          includeGlob: {
            type: "string",
            description: "Project-relative file glob.",
          },
        },
        ["pattern", "replacement"],
      ),
      mode: "read",
      risk: "read",
      execute: async ({ input, resource }) => {
        const includeGlob = stringArgument(input, "includeGlob");
        const result = await dependencies.invoke(
          "local_fs_regex_replace_preview",
          {
            projectRoot: resourceMetadataPath(resource, "projectRoot"),
            ...replaceArguments(input, "workspace.replace_preview"),
            includeGlob: includeGlob
              ? relativePath(
                  { includeGlob },
                  "includeGlob",
                  "workspace.replace_preview",
                )
              : null,
          },
        );
        return safeWorkspaceResults(result);
      },
    }),
    workspace({
      module: "workspace",
      tool: tool(
        "workspace.replace_apply",
        "Apply a previously reviewed regular-expression replacement to explicit files.",
        {
          pattern: { type: "string" },
          replacement: { type: "string" },
          caseSensitive: { type: "boolean" },
          wholeWord: { type: "boolean" },
          filePaths: { type: "array", items: { type: "string" }, minItems: 1 },
        },
        ["pattern", "replacement", "filePaths"],
      ),
      mode: "write",
      risk: "write",
      approvalPreview: ({ input }) => {
        const count = Array.isArray(input.filePaths)
          ? input.filePaths.length
          : 0;
        return `Apply workspace replacement to ${count} reviewed file(s). Replacement text redacted.`;
      },
      execute: ({ input, resource }) => {
        const filePaths = Array.isArray(input.filePaths)
          ? input.filePaths.map((path) =>
              safeWorkspacePath({ path }, "path", "workspace.replace_apply"),
            )
          : [];
        if (!filePaths.length)
          throw new Error("workspace.replace_apply requires filePaths");
        return dependencies.invoke("local_fs_regex_replace_apply", {
          projectRoot: resourceMetadataPath(resource, "projectRoot"),
          ...replaceArguments(input, "workspace.replace_apply"),
          filePaths,
        });
      },
    }),
    workspace({
      module: "workspace",
      tool: tool(
        "workspace.run_command",
        "Run a shell command in the bound project.",
        {
          command: { type: "string" },
          timeoutSecs: { type: "integer", minimum: 1, maximum: 300 },
        },
        ["command"],
      ),
      mode: "write",
      risk: "dangerous",
      approvalPreview: () =>
        "Run the supplied command in the bound project (command redacted).",
      execute: ({ input, resource, signal }) =>
        dependencies.invoke("workspace_exec", {
          cmd: requiredString(input, "command", "workspace.run_command"),
          cwd: resourceMetadataPath(resource, "projectRoot"),
          timeoutSecs:
            typeof input.timeoutSecs === "number"
              ? Math.max(1, Math.min(300, Math.round(input.timeoutSecs)))
              : 90,
        }, { signal }),
    }),
    workspace({
      module: "workspace",
      tool: tool(
        "workspace.create_directory",
        "Create a directory.",
        { path: { type: "string" } },
        ["path"],
      ),
      mode: "write",
      risk: "write",
      approvalPreview: ({ input }) =>
        relativePath(input, "path", "workspace.create_directory"),
      execute: ({ input, resource }) =>
        dependencies.invoke("local_fs_mkdir", {
          projectRoot: resourceMetadataPath(resource, "projectRoot"),
          dirPath: relativePath(input, "path", "workspace.create_directory"),
        }),
    }),
    workspace({
      module: "workspace",
      tool: tool(
        "workspace.rename_entry",
        "Rename a file or directory.",
        { path: { type: "string" }, newName: { type: "string" } },
        ["path", "newName"],
      ),
      mode: "write",
      risk: "write",
      approvalPreview: ({ input }) =>
        `${relativePath(input, "path", "workspace.rename_entry")} -> ${requiredString(input, "newName", "workspace.rename_entry")}`,
      execute: ({ input, resource }) =>
        dependencies.invoke("local_fs_rename", {
          projectRoot: resourceMetadataPath(resource, "projectRoot"),
          oldPath: relativePath(input, "path", "workspace.rename_entry"),
          newName: requiredString(input, "newName", "workspace.rename_entry"),
        }),
    }),
    workspace({
      module: "workspace",
      tool: tool(
        "workspace.delete_entry",
        "Delete a file or directory recursively.",
        { path: { type: "string" } },
        ["path"],
      ),
      mode: "write",
      risk: "dangerous",
      approvalPreview: ({ input }) =>
        relativePath(input, "path", "workspace.delete_entry"),
      execute: ({ input, resource }) =>
        dependencies.invoke("local_fs_delete", {
          projectRoot: resourceMetadataPath(resource, "projectRoot"),
          path: relativePath(input, "path", "workspace.delete_entry"),
        }),
    }),
    ...(["copy", "move"] as const).map((action) =>
      workspace({
        module: "workspace",
        tool: tool(
          `workspace.${action}_entry`,
          `${action} a file or directory.`,
          {
            path: { type: "string" },
            destinationDirectory: { type: "string" },
          },
          ["path", "destinationDirectory"],
        ),
        mode: "write",
        risk: "write",
        approvalPreview: ({ input }) =>
          `${relativePath(input, "path", `workspace.${action}_entry`)} -> ${relativePath(input, "destinationDirectory", `workspace.${action}_entry`)}`,
        execute: ({ input, resource }) =>
          dependencies.invoke(
            action === "copy" ? "local_fs_copy_to" : "local_fs_move_to",
            {
              projectRoot: resourceMetadataPath(resource, "projectRoot"),
              srcPath: relativePath(input, "path", `workspace.${action}_entry`),
              destDir: relativePath(
                input,
                "destinationDirectory",
                `workspace.${action}_entry`,
              ),
            },
          ),
      }),
    ),
    workspace({
      module: "workspace",
      tool: tool(
        "workspace.stat",
        "Read file metadata.",
        { path: { type: "string" } },
        ["path"],
      ),
      mode: "read",
      risk: "read",
      execute: async ({ input, resource }) =>
        sanitizeAiResult(
          await dependencies.invoke("local_fs_file_info", {
            projectRoot: resourceMetadataPath(resource, "projectRoot"),
            filePath: safeWorkspacePath(input, "path", "workspace.stat"),
          }),
        ),
    }),
    workspace({
      module: "workspace",
      tool: tool(
        "workspace.list_language_servers",
        "List the language servers this app knows about, with whether each is installed and already running for the bound project.",
      ),
      mode: "read",
      risk: "read",
      execute: async () =>
        sanitizeAiResult(
          withoutPluginLanguageServers(
            await dependencies.invoke("project_lsp_list_servers", {}),
          ),
        ),
    }),
    workspace({
      module: "workspace",
      tool: tool(
        "workspace.start_language_server",
        "Start a language server for the bound project. Returns a generation id that workspace.language_server_query and workspace.language_server_notify require.",
        { serverId },
        ["serverId"],
      ),
      mode: "write",
      risk: "write",
      approvalPreview: ({ input }) =>
        `Start language server ${builtinLanguageServerId(input, "workspace.start_language_server")}`,
      execute: async ({ input, resource }) =>
        sanitizeAiResult(
          await dependencies.invoke("project_lsp_start", {
            root: resourceMetadataPath(resource, "projectRoot"),
            serverId: builtinLanguageServerId(input,
              "workspace.start_language_server",
            ),
          }),
        ),
    }),
    workspace({
      module: "workspace",
      tool: tool(
        "workspace.stop_language_server",
        "Stop a running language server for the bound project.",
        { serverId },
        ["serverId"],
      ),
      mode: "write",
      risk: "write",
      approvalPreview: ({ input }) =>
        `Stop language server ${builtinLanguageServerId(input, "workspace.stop_language_server")}`,
      execute: ({ input, resource }) =>
        dependencies.invoke("project_lsp_stop", {
          root: resourceMetadataPath(resource, "projectRoot"),
          serverId: builtinLanguageServerId(input,
            "workspace.stop_language_server",
          ),
        }),
    }),
    workspace({
      module: "workspace",
      tool: tool(
        "workspace.language_server_logs",
        "Read the stderr buffer of a language server started for the bound project, to diagnose a server that installed but will not run.",
        { serverId },
        ["serverId"],
      ),
      mode: "read",
      risk: "read",
      execute: async ({ input, resource }) =>
        sanitizeAiResult(
          await dependencies.invoke("project_lsp_logs", {
            root: resourceMetadataPath(resource, "projectRoot"),
            serverId: builtinLanguageServerId(input,
              "workspace.language_server_logs",
            ),
          }),
        ),
    }),
    workspace({
      module: "workspace",
      tool: tool(
        "workspace.install_language_server",
        "Install a language server using the fixed command this app ships for it. Runs a package manager on this machine.",
        { serverId },
        ["serverId"],
      ),
      mode: "write",
      risk: "dangerous",
      approvalPreview: ({ input }) =>
        `Install language server ${builtinLanguageServerId(input, "workspace.install_language_server")} using its built-in install command`,
      execute: async ({ input }) =>
        sanitizeAiResult(
          await dependencies.invoke("project_lsp_install", {
            serverId: builtinLanguageServerId(input,
              "workspace.install_language_server",
            ),
          }),
        ),
    }),
    workspace({
      module: "workspace",
      /**
       * 后端 project_lsp_request 能发任意 LSP 方法，包含 workspace/executeCommand
       * 这种会让语言服务器代跑代码的。这里只放查询类方法，写操作不从 AI 走。
       */
      tool: tool(
        "workspace.language_server_query",
        "Ask a running language server a read-only question such as a definition, references or hover. Open the file with workspace.language_server_notify first.",
        {
          serverId,
          generation: {
            type: "integer",
            description:
              "Generation id returned by workspace.start_language_server.",
          },
          method: {
            type: "string",
            enum: LSP_QUERY_METHODS,
            description: "LSP request method.",
          },
          params: {
            type: "object",
            description:
              "LSP params for the method, using file:// uris inside the bound project.",
          },
        },
        ["serverId", "generation", "method", "params"],
      ),
      mode: "read",
      risk: "read",
      execute: async ({ input, resource }) =>
        sanitizeAiResult(
          await dependencies.invoke("project_lsp_request", {
            root: resourceMetadataPath(resource, "projectRoot"),
            serverId: builtinLanguageServerId(input,
              "workspace.language_server_query",
            ),
            generation: lspGeneration(input),
            method: lspMethod(
              input,
              LSP_QUERY_METHODS,
              "workspace.language_server_query",
            ),
            params: lspParams(
              input,
              resourceMetadataPath(resource, "projectRoot"),
            ),
          }),
        ),
    }),
    workspace({
      module: "workspace",
      tool: tool(
        "workspace.language_server_notify",
        "Tell a running language server that a document opened, changed, saved or closed, so queries about it return results.",
        {
          serverId,
          generation: {
            type: "integer",
            description:
              "Generation id returned by workspace.start_language_server.",
          },
          method: {
            type: "string",
            enum: LSP_SYNC_METHODS,
            description: "LSP document synchronization notification.",
          },
          params: {
            type: "object",
            description:
              "LSP params for the notification, using file:// uris inside the bound project.",
          },
        },
        ["serverId", "generation", "method", "params"],
      ),
      mode: "write",
      risk: "write",
      approvalPreview: ({ input }) =>
        `Send ${lspMethod(input, LSP_SYNC_METHODS, "workspace.language_server_notify")} to language server ${builtinLanguageServerId(input, "workspace.language_server_notify")}`,
      execute: ({ input, resource }) =>
        dependencies.invoke("project_lsp_notify", {
          root: resourceMetadataPath(resource, "projectRoot"),
          serverId: builtinLanguageServerId(input,
            "workspace.language_server_notify",
          ),
          generation: lspGeneration(input),
          method: lspMethod(
            input,
            LSP_SYNC_METHODS,
            "workspace.language_server_notify",
          ),
          params: lspParams(
            input,
            resourceMetadataPath(resource, "projectRoot"),
          ),
        }),
    }),
  ];
}
