import type { AiCapability } from "@/domain/ai/capability-registry";
import type { WorkspaceView } from "@/types/workspace";
import { relativePath, requiredString, tool } from "./shared";

const AI_OPENABLE_WORKSPACE_VIEWS = [
  "dashboard",
  "terminal",
  "ssh",
  "database",
  "sftp",
  "http",
  "sessions",
] as const satisfies readonly WorkspaceView[];

type AiOpenableWorkspaceView = (typeof AI_OPENABLE_WORKSPACE_VIEWS)[number];

export interface WorkspaceAiDependencies {
  openWorkspaceView?(input: {
    view: AiOpenableWorkspaceView;
  }): Promise<unknown>;
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

export function workspaceCapabilities({ openWorkspaceView }: WorkspaceAiDependencies): AiCapability[] {
  if (!openWorkspaceView) return [];
  return [{
    module: "workspace",
    discovery: true,
    target: "none",
    tool: tool(
      "workspace.open_view",
      "Open and focus a MinCode workspace page or tab.",
      { view: { type: "string", enum: AI_OPENABLE_WORKSPACE_VIEWS } },
      ["view"],
    ),
    mode: "read",
    risk: "read",
    execute: ({ input }) => {
      const view = requiredString(input, "view", "workspace.open_view") as AiOpenableWorkspaceView;
      if (!AI_OPENABLE_WORKSPACE_VIEWS.includes(view)) {
        throw new Error(`workspace.open_view does not support view: ${view}`);
      }
      return openWorkspaceView({ view });
    },
  }];
}
