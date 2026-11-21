import type { AiCapability } from "@/domain/ai/capability-registry";
import { requiredString, sanitizeAiResult, tool, type AiToolDependencies } from "./shared";

export function sessionsCapabilities(dependencies: AiToolDependencies, nativeRuntime?: boolean): AiCapability[] {
  if (!nativeRuntime) return [];
  const invoke = dependencies.invoke;
  return [
    { module: "sessions", target: "none", discovery: true, tool: tool("sessions.list_claude_projects", "List local Claude session projects."), mode: "read", risk: "read", execute: async () => sanitizeAiResult(await invoke("list_claude_projects", {})) },
    { module: "sessions", target: "none", tool: tool("sessions.list_claude", "List local Claude sessions for one encoded project directory.", { encodedDir: { type: "string" } }, ["encodedDir"]), mode: "read", risk: "read", execute: async ({ input }) => sanitizeAiResult(await invoke("get_project_sessions", { encodedDir: requiredString(input, "encodedDir", "sessions.list_claude") })) },
    { module: "sessions", target: "none", tool: tool("sessions.list_codex", "List local Codex sessions."), mode: "read", risk: "read", execute: async () => sanitizeAiResult(await invoke("list_codex_sessions", {})) },
    { module: "sessions", target: "none", tool: tool("sessions.get_claude_messages", "Read messages for one local Claude session.", { encodedDir: { type: "string" }, uuid: { type: "string" }, limit: { type: "integer", minimum: 1, maximum: 200 } }, ["encodedDir", "uuid"]), mode: "read", risk: "read", execute: async ({ input }) => sanitizeAiResult(await invoke("get_session_messages", { encodedDir: requiredString(input, "encodedDir", "sessions.get_claude_messages"), uuid: requiredString(input, "uuid", "sessions.get_claude_messages"), limit: typeof input.limit === "number" ? Math.min(200, Math.max(1, Math.floor(input.limit))) : 200 })) },
    { module: "sessions", target: "none", tool: tool("sessions.get_codex_messages", "Read messages for one local Codex session file.", { filePath: { type: "string" } }, ["filePath"]), mode: "read", risk: "read", execute: async ({ input }) => sanitizeAiResult(await invoke("get_codex_session_messages", { filePath: requiredString(input, "filePath", "sessions.get_codex_messages") })) },
    {
      module: "sessions", target: "none", tool: tool("sessions.rename_claude", "Rename one local Claude session using sidecar metadata.", { encodedDir: { type: "string" }, uuid: { type: "string" }, title: { type: "string", maxLength: 140 } }, ["encodedDir", "uuid", "title"]), mode: "write", risk: "write",
      approvalPreview: ({ input }) => `Rename Claude session ${requiredString(input, "uuid", "sessions.rename_claude")} (${requiredString(input, "title", "sessions.rename_claude").length} characters)`,
      execute: ({ input }) => invoke("rename_claude_session", { encodedDir: requiredString(input, "encodedDir", "sessions.rename_claude"), uuid: requiredString(input, "uuid", "sessions.rename_claude"), title: requiredString(input, "title", "sessions.rename_claude") }),
    },
    {
      module: "sessions", target: "none", tool: tool("sessions.delete_claude", "Delete one local Claude session.", { encodedDir: { type: "string" }, uuid: { type: "string" } }, ["encodedDir", "uuid"]), mode: "write", risk: "dangerous",
      approvalPreview: ({ input }) => `Delete Claude session ${requiredString(input, "uuid", "sessions.delete_claude")}`,
      execute: ({ input }) => invoke("delete_claude_session", { encodedDir: requiredString(input, "encodedDir", "sessions.delete_claude"), uuid: requiredString(input, "uuid", "sessions.delete_claude") }),
    },
    {
      module: "sessions", target: "none", tool: tool("sessions.delete_codex", "Delete one local Codex session file.", { filePath: { type: "string" } }, ["filePath"]), mode: "write", risk: "dangerous",
      approvalPreview: ({ input }) => `Delete Codex session ${requiredString(input, "filePath", "sessions.delete_codex").split(/[\\/]/).pop()}`,
      execute: ({ input }) => invoke("delete_codex_session", { filePath: requiredString(input, "filePath", "sessions.delete_codex") }),
    },
  ];
}
