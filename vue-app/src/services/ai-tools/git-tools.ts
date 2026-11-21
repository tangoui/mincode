import type { AiCapability, AiCapabilityRisk } from "@/domain/ai/capability-registry";
import {
  relativePath, requiredString, resourceCapability, resourceMetadataPath, stringArgument, tool,
  type AiToolDependencies,
} from "./shared";

export const gitCapabilityNames = [
  "git.status", "git.log", "git.diff", "git.branches", "git.tags", "git.remotes", "git.show_commit", "git.commit_file_diff",
  "git.blame", "git.conflict_list", "git.conflict_read", "git.changelog", "git.stage", "git.unstage", "git.stage_all",
  "git.unstage_all", "git.discard", "git.discard_all", "git.commit", "git.amend", "git.undo_last_commit", "git.create_branch",
  "git.delete_branch", "git.rename_branch", "git.checkout_branch", "git.merge", "git.create_tag", "git.delete_tag", "git.revert",
  "git.cherry_pick", "git.conflict_write", "git.interactive_rebase", "git.pull", "git.push", "git.stash_list", "git.stash_create",
  "git.stash_pop", "git.stash_drop", "git.worktree_list", "git.worktree_add", "git.worktree_remove", "git.worktree_prune",
  "git.worktree_lock", "git.worktree_unlock", "git.tracking_status", "git.staged_patch", "git.read_file_at_ref",
  "git.list_repositories", "git.reset", "git.revert_hunk",
];

const sensitiveFileNames = new Set([
  ".env", ".npmrc", ".pypirc", ".netrc", "id_rsa", "id_ed25519", "credentials",
]);

function isSensitiveGitPath(path: string) {
  const name = path.split("/").pop()?.toLowerCase() ?? "";
  return sensitiveFileNames.has(name) || name.startsWith(".env.") || /\.(pem|key|p12|pfx)$/i.test(name);
}

function safeGitPath(input: Record<string, unknown>, key: string, capability: string) {
  const path = relativePath(input, key, capability);
  if (isSensitiveGitPath(path)) {
    throw new Error(`${capability} cannot access sensitive files`);
  }
  return path;
}

/**
 * 整包暂存 diff 里可能夹着 .env 之类，git.diff 逐文件挡住的东西不能从这里漏出去。
 * 按 `diff --git` 切段，丢掉命中敏感规则的文件段。
 */
function redactSensitivePatch(patch: unknown) {
  if (typeof patch !== "string" || !patch) return "";
  const sections = patch.split(/(?=^diff --git )/m);
  const kept: string[] = [];
  let redacted = 0;
  for (const section of sections) {
    const header = /^diff --git a\/(.+?) b\/(.+?)$/m.exec(section);
    const paths = header ? [header[1], header[2]] : [];
    if (paths.some(isSensitiveGitPath)) {
      redacted += 1;
      continue;
    }
    kept.push(section);
  }
  return redacted ? `${kept.join("")}\n... (${redacted} sensitive file section(s) redacted)` : kept.join("");
}

function safeRemoteUrl(value: unknown) {
  if (typeof value !== "string") return "";
  try {
    const url = new URL(value);
    url.username = "";
    url.password = "";
    return url.toString();
  } catch {
    return value.replace(/^(https?:\/\/)[^/@]+@/i, "$1");
  }
}

function rebaseActions(input: Record<string, unknown>) {
  if (!Array.isArray(input.actions) || !input.actions.length) {
    throw new Error("git.interactive_rebase requires actions");
  }
  const allowed = new Set(["pick", "squash", "fixup", "drop"]);
  return input.actions.map((entry) => {
    if (!entry || typeof entry !== "object") throw new Error("git.interactive_rebase has an invalid action");
    const action = entry as Record<string, unknown>;
    const kind = requiredString(action, "action", "git.interactive_rebase");
    if (!allowed.has(kind)) throw new Error("git.interactive_rebase has an unsupported action");
    const hash = requiredString(action, "hash", "git.interactive_rebase");
    const message = stringArgument(action, "message");
    if (/[\r\n]/.test(hash) || /[\r\n]/.test(message)) {
      throw new Error("git.interactive_rebase action fields cannot contain line breaks");
    }
    return { hash, action: kind, message };
  });
}

export function gitCapabilities(dependencies: AiToolDependencies): AiCapability[] {
  const git = resourceCapability;
  const repoPath = resourceMetadataPath;
  const invoke = (command: string, resource: Parameters<typeof repoPath>[0], args: Record<string, unknown> = {}) =>
    dependencies.invoke(command, { repoPath: repoPath(resource, "repoPath"), ...args });
  const simpleRead = (name: string, command: string, args: Record<string, unknown> = {}) => git({
    module: "git", tool: tool(name, `Read ${name} from the bound Git repository.`), mode: "read", risk: "read",
    execute: ({ resource }) => invoke(command, resource, args),
  });
  const mutation = (config: {
    name: string; command: string; risk: AiCapabilityRisk; properties?: Record<string, unknown>; required?: string[];
    preview?: (input: Record<string, unknown>) => string;
    args(input: Record<string, unknown>): Record<string, unknown>;
  }) => git({ module: "git", tool: tool(config.name, `Run ${config.name} in the bound Git repository.`, config.properties, config.required),
    mode: "write", risk: config.risk, approvalPreview: ({ input }) => config.preview?.(input) ?? config.name,
    execute: ({ input, resource }) => invoke(config.command, resource, config.args(input)) });
  const branch = { type: "string" };
  const path = { type: "string", description: "Repository-relative non-sensitive path." };
  const hash = { type: "string" };
  const index = { type: "integer", minimum: 0 };
  const pathMutation = (name: string, command: string, risk: AiCapabilityRisk) => mutation({
    name, command, risk, properties: { path }, required: ["path"],
    preview: (input) => safeGitPath(input, "path", name),
    args: (input) => ({ relativePath: safeGitPath(input, "path", name) }),
  });

  return [
    simpleRead("git.status", "git_get_status"),
    git({ module: "git", tool: tool("git.log", "Read commit history.", { limit: { type: "integer", minimum: 1, maximum: 500 } }), mode: "read", risk: "read",
      execute: ({ input, resource }) => invoke("git_get_log", resource, { limit: typeof input.limit === "number" ? input.limit : 100 }) }),
    git({ module: "git", tool: tool("git.diff", "Read a non-sensitive file diff.", { path, staged: { type: "boolean" }, oldPath: path }, ["path"]), mode: "read", risk: "read",
      execute: ({ input, resource }) => invoke("git_get_diff", resource, { relativePath: safeGitPath(input, "path", "git.diff"), staged: input.staged === true,
        oldPath: stringArgument(input, "oldPath") ? safeGitPath(input, "oldPath", "git.diff") : "" }) }),
    simpleRead("git.branches", "git_list_branches"), simpleRead("git.tags", "git_list_tags"),
    git({ module: "git", tool: tool("git.remotes", "Read a credential-free remote URL.", { remote: { type: "string" } }), mode: "read", risk: "read",
      execute: async ({ input, resource }) => safeRemoteUrl(await invoke("git_get_remote_url", resource, { remoteName: stringArgument(input, "remote") || null })) }),
    git({ module: "git", tool: tool("git.show_commit", "Read commit metadata and changed file names.", { hash }, ["hash"]), mode: "read", risk: "read",
      execute: ({ input, resource }) => invoke("git_get_commit_detail", resource, { commitHash: requiredString(input, "hash", "git.show_commit") }) }),
    git({ module: "git", tool: tool("git.commit_file_diff", "Read a non-sensitive file diff from a commit.", { hash, path, oldPath: path }, ["hash", "path"]), mode: "read", risk: "read",
      execute: ({ input, resource }) => invoke("git_get_commit_file_diff", resource, { commitHash: requiredString(input, "hash", "git.commit_file_diff"),
        relativePath: safeGitPath(input, "path", "git.commit_file_diff"), oldPath: stringArgument(input, "oldPath") ? safeGitPath(input, "oldPath", "git.commit_file_diff") : "" }) }),
    git({ module: "git", tool: tool("git.blame", "Read blame information.", { path }, ["path"]), mode: "read", risk: "read",
      execute: ({ input, resource }) => invoke("git_blame", resource, { relativePath: safeGitPath(input, "path", "git.blame") }) }),
    simpleRead("git.conflict_list", "git_list_conflicts"),
    git({ module: "git", tool: tool("git.conflict_read", "Read a non-sensitive conflicted file.", { path }, ["path"]), mode: "read", risk: "read",
      execute: ({ input, resource }) => invoke("git_get_conflict_content", resource, { relativePath: safeGitPath(input, "path", "git.conflict_read") }) }),
    simpleRead("git.changelog", "git_generate_changelog"),
    pathMutation("git.stage", "git_stage_file", "write"), pathMutation("git.unstage", "git_unstage_file", "write"),
    mutation({ name: "git.stage_all", command: "git_stage_all", risk: "write", args: () => ({}) }),
    mutation({ name: "git.unstage_all", command: "git_unstage_all", risk: "write", args: () => ({}) }),
    pathMutation("git.discard", "git_discard_file", "dangerous"),
    mutation({ name: "git.discard_all", command: "git_discard_all", risk: "dangerous", args: () => ({}) }),
    mutation({ name: "git.commit", command: "git_commit", risk: "write", properties: { message: { type: "string" } }, required: ["message"],
      preview: () => "Create a commit with the supplied message (message redacted).", args: (input) => ({ message: requiredString(input, "message", "git.commit") }) }),
    mutation({ name: "git.amend", command: "git_commit_amend", risk: "dangerous", properties: { message: { type: "string" } },
      preview: () => "Amend the current commit (message redacted).", args: (input) => ({ message: stringArgument(input, "message") }) }),
    mutation({ name: "git.undo_last_commit", command: "git_undo_last_commit", risk: "dangerous", args: () => ({}) }),
    mutation({ name: "git.create_branch", command: "git_checkout_branch", risk: "write", properties: { branch }, required: ["branch"],
      preview: (input) => requiredString(input, "branch", "git.create_branch"), args: (input) => ({ branchName: requiredString(input, "branch", "git.create_branch"), create: true, stashChanges: false }) }),
    mutation({ name: "git.delete_branch", command: "git_delete_branch", risk: "dangerous", properties: { branch, force: { type: "boolean" } }, required: ["branch"],
      preview: (input) => requiredString(input, "branch", "git.delete_branch"), args: (input) => ({ branchName: requiredString(input, "branch", "git.delete_branch"), force: input.force === true }) }),
    mutation({ name: "git.rename_branch", command: "git_rename_branch", risk: "write", properties: { oldName: branch, newName: branch }, required: ["oldName", "newName"],
      preview: (input) => `${requiredString(input, "oldName", "git.rename_branch")} -> ${requiredString(input, "newName", "git.rename_branch")}`,
      args: (input) => ({ oldName: requiredString(input, "oldName", "git.rename_branch"), newName: requiredString(input, "newName", "git.rename_branch") }) }),
    mutation({ name: "git.checkout_branch", command: "git_checkout_branch", risk: "write", properties: { branch, stashChanges: { type: "boolean" } }, required: ["branch"],
      preview: (input) => requiredString(input, "branch", "git.checkout_branch"), args: (input) => ({ branchName: requiredString(input, "branch", "git.checkout_branch"), create: false, stashChanges: input.stashChanges === true }) }),
    mutation({ name: "git.merge", command: "git_merge_branch", risk: "dangerous", properties: { branch }, required: ["branch"],
      preview: (input) => requiredString(input, "branch", "git.merge"), args: (input) => ({ branchName: requiredString(input, "branch", "git.merge") }) }),
    mutation({ name: "git.create_tag", command: "git_create_tag", risk: "write", properties: { name: { type: "string" }, message: { type: "string" } }, required: ["name"],
      preview: (input) => requiredString(input, "name", "git.create_tag"), args: (input) => ({ name: requiredString(input, "name", "git.create_tag"), message: stringArgument(input, "message") || null }) }),
    mutation({ name: "git.delete_tag", command: "git_delete_tag", risk: "dangerous", properties: { name: { type: "string" } }, required: ["name"],
      preview: (input) => requiredString(input, "name", "git.delete_tag"), args: (input) => ({ name: requiredString(input, "name", "git.delete_tag") }) }),
    ...(["revert", "cherry_pick"] as const).map((action) => mutation({ name: `git.${action}`, command: `git_${action}`, risk: "dangerous", properties: { hash }, required: ["hash"],
      preview: (input) => requiredString(input, "hash", `git.${action}`), args: (input) => ({ commitHash: requiredString(input, "hash", `git.${action}`) }) })),
    mutation({ name: "git.conflict_write", command: "git_resolve_conflict", risk: "dangerous", properties: { path, content: { type: "string" } }, required: ["path", "content"],
      preview: (input) => `${safeGitPath(input, "path", "git.conflict_write")} (content redacted)`,
      args: (input) => ({ relativePath: safeGitPath(input, "path", "git.conflict_write"), content: typeof input.content === "string" ? input.content : "" }) }),
    mutation({ name: "git.interactive_rebase", command: "git_interactive_rebase", risk: "dangerous", properties: {
      baseHash: hash, actions: { type: "array", minItems: 1, items: { type: "object", properties: { hash, action: { type: "string", enum: ["pick", "squash", "fixup", "drop"] }, message: { type: "string" } }, required: ["hash", "action"], additionalProperties: false } } },
      required: ["baseHash", "actions"], preview: (input) => `Rewrite commits after ${requiredString(input, "baseHash", "git.interactive_rebase")} (${Array.isArray(input.actions) ? input.actions.length : 0} actions; messages redacted).`,
      args: (input) => ({ baseHash: requiredString(input, "baseHash", "git.interactive_rebase"), actions: rebaseActions(input) }) }),
    ...(["pull", "push"] as const).map((action) => mutation({ name: `git.${action}`, command: `git_${action}`, risk: "dangerous",
      properties: { remote: { type: "string" }, branch }, preview: (input) => `${action} ${stringArgument(input, "remote") || "origin"} ${stringArgument(input, "branch") || "current branch"}`,
      args: (input) => ({ remote: stringArgument(input, "remote") || null, branch: stringArgument(input, "branch") || null, username: null, token: null }) })),
    simpleRead("git.stash_list", "git_stash_list"),
    mutation({ name: "git.stash_create", command: "git_stash_push", risk: "write", properties: { message: { type: "string" } }, preview: () => "Create stash (message redacted).", args: (input) => ({ message: stringArgument(input, "message") || null }) }),
    mutation({ name: "git.stash_pop", command: "git_stash_pop", risk: "write", properties: { index }, required: ["index"], preview: (input) => `stash@{${typeof input.index === "number" ? input.index : 0}}`, args: (input) => ({ stashIndex: typeof input.index === "number" ? input.index : 0 }) }),
    mutation({ name: "git.stash_drop", command: "git_stash_drop", risk: "dangerous", properties: { index }, required: ["index"], preview: (input) => `stash@{${typeof input.index === "number" ? input.index : 0}}`, args: (input) => ({ stashIndex: typeof input.index === "number" ? input.index : 0 }) }),
    simpleRead("git.tracking_status", "git_get_tracking_status"),
    git({ module: "git", tool: tool("git.staged_patch", "Read the staged diff as a patch, with sensitive file sections removed."), mode: "read", risk: "read",
      execute: async ({ resource }) => redactSensitivePatch(await invoke("git_get_staged_patch", resource)) }),
    git({ module: "git", tool: tool("git.read_file_at_ref", "Read a non-sensitive file's contents at a commit, branch or tag. Defaults to HEAD.", { path, ref: { type: "string", description: "Commit hash, branch or tag; omit for HEAD." } }, ["path"]), mode: "read", risk: "read",
      execute: ({ input, resource }) => {
        const ref = stringArgument(input, "ref");
        const relative = safeGitPath(input, "path", "git.read_file_at_ref");
        return ref
          ? invoke("git_read_file_at_ref", resource, { relativePath: relative, gitRef: ref })
          : invoke("git_read_file_at_head", resource, { relativePath: relative });
      } }),
    git({ module: "git", tool: tool("git.list_repositories", "List the Git repositories nested inside the bound project, without exposing absolute paths."), mode: "read", risk: "read",
      execute: async ({ resource }) => {
        const result = await dependencies.invoke("git_list_project_repositories", { projectRoot: repoPath(resource, "repoPath") });
        if (!Array.isArray(result)) return [];
        return result.map((entry) => {
          const record = entry && typeof entry === "object" ? entry as Record<string, unknown> : {};
          const { path: _path, ...safe } = record;
          return safe;
        });
      } }),
    mutation({ name: "git.reset", command: "git_reset", risk: "dangerous", properties: { hash, mode: { type: "string", enum: ["soft", "mixed", "hard"], description: "hard discards working tree changes irreversibly; defaults to mixed." } }, required: ["hash"],
      preview: (input) => {
        const mode = stringArgument(input, "mode") || "mixed";
        const target = requiredString(input, "hash", "git.reset");
        return mode === "hard"
          ? `git reset --hard ${target} — discards all uncommitted changes irreversibly`
          : `git reset --${mode} ${target}`;
      },
      args: (input) => ({ commitHash: requiredString(input, "hash", "git.reset"), mode: stringArgument(input, "mode") || "mixed" }) }),
    mutation({ name: "git.revert_hunk", command: "git_revert_diff_hunk", risk: "dangerous", properties: { path, hunkIndex: index, staged: { type: "boolean" } }, required: ["path", "hunkIndex"],
      preview: (input) => `Revert hunk #${typeof input.hunkIndex === "number" ? input.hunkIndex : 0} in ${safeGitPath(input, "path", "git.revert_hunk")}`,
      args: (input) => ({ relativePath: safeGitPath(input, "path", "git.revert_hunk"), hunkIndex: typeof input.hunkIndex === "number" ? input.hunkIndex : 0, staged: input.staged === true }) }),
    git({ module: "git", tool: tool("git.worktree_list", "List worktrees without exposing absolute paths."), mode: "read", risk: "read", execute: async ({ resource }) => {
      const result = await invoke("git_list_worktrees", resource);
      if (!Array.isArray(result)) return [];
      return result.map((entry) => {
        const record = entry && typeof entry === "object" ? entry as Record<string, unknown> : {};
        const rawPath = typeof record.path === "string" ? record.path : "";
        const { path: _path, ...safe } = record;
        return { ...safe, name: rawPath.split(/[\\/]/).filter(Boolean).pop() ?? "worktree" };
      });
    } }),
    mutation({ name: "git.worktree_add", command: "git_add_worktree", risk: "dangerous", properties: { path: { type: "string" }, branch, newBranch: { type: "boolean" }, baseBranch: { ...branch, description: "Branch the new branch starts from. Only used when newBranch is true; defaults to HEAD." } }, required: ["path"],
      preview: (input) => `Add worktree at a user-selected path for ${stringArgument(input, "branch") || "HEAD"}. Path redacted.`, args: (input) => ({ worktreePath: requiredString(input, "path", "git.worktree_add"), branch: stringArgument(input, "branch") || null, baseBranch: stringArgument(input, "baseBranch") || null, newBranch: input.newBranch === true }) }),
    mutation({ name: "git.worktree_remove", command: "git_remove_worktree", risk: "dangerous", properties: { path: { type: "string" }, force: { type: "boolean" } }, required: ["path"], preview: () => "Remove worktree at the supplied path (path redacted).", args: (input) => ({ worktreePath: requiredString(input, "path", "git.worktree_remove"), force: input.force === true }) }),
    mutation({ name: "git.worktree_prune", command: "git_prune_worktrees", risk: "write", args: () => ({}) }),
    mutation({ name: "git.worktree_lock", command: "git_lock_worktree", risk: "write", properties: { path: { type: "string" }, reason: { type: "string" } }, required: ["path"], preview: () => "Lock worktree at the supplied path (path and reason redacted).", args: (input) => ({ worktreePath: requiredString(input, "path", "git.worktree_lock"), reason: stringArgument(input, "reason") || null }) }),
    mutation({ name: "git.worktree_unlock", command: "git_unlock_worktree", risk: "write", properties: { path: { type: "string" } }, required: ["path"], preview: () => "Unlock worktree at the supplied path (path redacted).", args: (input) => ({ worktreePath: requiredString(input, "path", "git.worktree_unlock") }) }),
  ];
}
