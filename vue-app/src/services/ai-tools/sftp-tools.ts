import { aiResourceSummary } from "./shared";
import type { AiCapability } from "@/domain/ai/capability-registry";
import type { AiResource } from "@/domain/ai/resource-registry";
import type { TerminalAiDependencies } from "./terminal-tools";
import { resourceMetadataPath, sanitizeAiResult, tool } from "./shared";
import { safeWorkspacePath } from "./workspace-tools";

export interface SftpAiDependencies extends TerminalAiDependencies {
  connectSftp?(profileId: string): Promise<{ sessionId: string; home: string }>;
  disconnectSftp?(sessionId: string): Promise<void>;
}

function text(input: Record<string, unknown>, key: string) {
  return typeof input[key] === "string" ? input[key].trim() : "";
}

function content(input: Record<string, unknown>, capability: string) {
  if (typeof input.content !== "string") throw new Error(`${capability} requires content`);
  return input.content;
}

const sensitiveFileNames = new Set([
  ".env", ".npmrc", ".pypirc", ".netrc", "id_rsa", "id_ed25519", "credentials",
]);

function safeRemotePath(input: Record<string, unknown>, key: string, capability: string) {
  const path = text(input, key).replace(/\\/g, "/");
  if (!path) throw new Error(`${capability} requires ${key}`);
  const segments = path.toLowerCase().split("/").filter(Boolean);
  const name = segments[segments.length - 1] ?? "";
  if (segments.some((segment) => sensitiveFileNames.has(segment) || segment.startsWith(".env."))
    || /\.(pem|key|p12|pfx|kdbx)$/i.test(name)) {
    throw new Error(`${capability} cannot access sensitive files`);
  }
  return path;
}

function sessionId(resource: AiResource) {
  const value = resource.metadata?.sessionId;
  if (typeof value !== "string" || !value) throw new Error("SFTP resource has no bound session");
  return value;
}

const targetProperties = {
  resourceId: { type: "string", description: "Stable connected SFTP session resource id." },
  name: { type: "string", description: "Unique connected SFTP session name." },
};

function schema(properties: Record<string, unknown>, required: string[] = []) {
  return {
    type: "object",
    properties: { ...targetProperties, ...properties },
    ...(required.length ? { required } : {}),
    additionalProperties: false,
  };
}

export const sftpProfileCapabilityNames = ["sftp.connect"];
export const sftpWorkspaceTransferCapabilityNames = ["sftp.workspace_transfer"];

export const sftpCapabilityNames = [
  "sftp.disconnect",
  "sftp.list",
  "sftp.read",
  "sftp.write",
  "sftp.mkdir",
  "sftp.rename",
  "sftp.delete",
  "sftp.upload",
  "sftp.home",
  "sftp.list_transfers",
  "sftp.cancel_transfer",
];

function transferDirection(input: Record<string, unknown>) {
  if (input.direction !== "upload") {
    throw new Error("sftp.workspace_transfer currently supports upload only");
  }
  return input.direction;
}

function transferConflictStrategy(input: Record<string, unknown>) {
  if (input.conflictStrategy === "overwrite" || input.conflictStrategy === "auto-rename") {
    return input.conflictStrategy;
  }
  return "skip";
}

function transferJobId() {
  return typeof crypto.randomUUID === "function"
    ? `ai-${crypto.randomUUID()}`
    : `ai-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

export function sftpWorkspaceTransferCapabilities(dependencies: SftpAiDependencies): AiCapability[] {
  return [{
    module: "sftp",
    tool: tool("sftp.workspace_transfer", "Upload one non-sensitive file from the bound workspace project to the bound SFTP session without exposing a local absolute path.", {
      direction: { type: "string", enum: ["upload"] },
      workspacePath: { type: "string", description: "File path relative to the bound workspace project." },
      remotePath: { type: "string", description: "Path in the bound SFTP session." },
      resume: { type: "boolean" },
      conflictStrategy: { type: "string", enum: ["skip", "auto-rename", "overwrite"], description: "Defaults to skip. Overwrite requires the same approval as the transfer." },
    }, ["direction", "workspacePath", "remotePath"]),
    mode: "write",
    risk: "dangerous",
    approvalPreview: ({ input }) => {
      transferDirection(input);
      const workspacePath = safeWorkspacePath(input, "workspacePath", "sftp.workspace_transfer");
      const remotePath = safeRemotePath(input, "remotePath", "sftp.workspace_transfer");
      return `Upload workspace file ${workspacePath} to ${remotePath}`;
    },
    execute: async ({ input, resource }) => sanitizeAiResult(await dependencies.invoke("sftp_workspace_transfer_start", {
      jobId: transferJobId(),
      sessionId: sessionId(resource),
      projectRoot: resourceMetadataPath(resource, "projectRoot"),
      direction: transferDirection(input),
      workspacePath: safeWorkspacePath(input, "workspacePath", "sftp.workspace_transfer"),
      remotePath: safeRemotePath(input, "remotePath", "sftp.workspace_transfer"),
      resume: input.resume === true,
      conflictStrategy: transferConflictStrategy(input),
    })),
  }];
}

export function sftpCapabilities(dependencies: SftpAiDependencies): AiCapability[] {
  const invoke = (command: string, resource: AiResource, payload: Record<string, unknown>) =>
    dependencies.invoke(command, { sessionId: sessionId(resource), ...payload });
  return [
    ...(dependencies.connectSftp
      ? [{
          module: "sftp",
          mode: "write" as const,
          risk: "network" as const,
          tool: {
            name: "sftp.connect",
            description: "Connect SFTP using an internally resolved saved SSH profile and credentials.",
            inputSchema: schema({}),
          },
          execute: ({ resource }: { resource: AiResource }) => {
            const profileId = resource.metadata?.profileId;
            if (typeof profileId !== "string" || !profileId) throw new Error("SFTP profile resource is invalid");
            return dependencies.connectSftp!(profileId);
          },
        }]
      : []),
    {
      module: "sftp", discovery: true, target: "none", mode: "read", risk: "read",
      tool: { name: "sftp.list_sessions", description: "List connected SFTP sessions.", inputSchema: { type: "object", properties: {}, additionalProperties: false } },
      execute: ({ externalAgent, resources }) =>
        (externalAgent ? resources?.list?.("sftp.list")?.map(aiResourceSummary) : undefined)
        ?? dependencies.listResources("sftp.list"),
    },
    ...(dependencies.disconnectSftp
      ? [{
          module: "sftp",
          mode: "write" as const,
          risk: "write" as const,
          tool: { name: "sftp.disconnect", description: "Disconnect a connected SFTP session.", inputSchema: schema({}) },
          execute: ({ resource }: { resource: AiResource }) => dependencies.disconnectSftp!(sessionId(resource)),
        }]
      : []),
    {
      module: "sftp", mode: "read", risk: "read",
      tool: { name: "sftp.list", description: "List a remote directory in a connected SFTP session.", inputSchema: schema({ path: { type: "string" } }, ["path"]) },
      execute: async ({ input, resource }) => sanitizeAiResult(await invoke("sftp_list_dir", resource, { path: safeRemotePath(input, "path", "sftp.list") })),
    },
    {
      module: "sftp", mode: "read", risk: "read",
      tool: { name: "sftp.read", description: "Read a UTF-8 remote text file up to the backend limit.", inputSchema: schema({ path: { type: "string" } }, ["path"]) },
      execute: async ({ input, resource }) => sanitizeAiResult(await invoke("sftp_read_text_file", resource, { path: safeRemotePath(input, "path", "sftp.read") })),
    },
    {
      module: "sftp", mode: "write", risk: "dangerous",
      tool: { name: "sftp.write", description: "Overwrite a remote text file.", inputSchema: schema({ path: { type: "string" }, content: { type: "string" } }, ["path", "content"]) },
      approvalPreview: ({ input }) => safeRemotePath(input, "path", "sftp.write"),
      execute: ({ input, resource }) => invoke("sftp_write_text_file", resource, { path: safeRemotePath(input, "path", "sftp.write"), content: content(input, "sftp.write") }),
    },
    {
      module: "sftp", mode: "write", risk: "write",
      tool: { name: "sftp.mkdir", description: "Create remote directories recursively.", inputSchema: schema({ path: { type: "string" } }, ["path"]) },
      approvalPreview: ({ input }) => safeRemotePath(input, "path", "sftp.mkdir"),
      execute: ({ input, resource }) => invoke("sftp_mkdirs", resource, { path: safeRemotePath(input, "path", "sftp.mkdir") }),
    },
    {
      module: "sftp", mode: "write", risk: "dangerous",
      tool: { name: "sftp.rename", description: "Rename or move a remote entry.", inputSchema: schema({ oldPath: { type: "string" }, newPath: { type: "string" } }, ["oldPath", "newPath"]) },
      approvalPreview: ({ input }) => `${safeRemotePath(input, "oldPath", "sftp.rename")} -> ${safeRemotePath(input, "newPath", "sftp.rename")}`,
      execute: ({ input, resource }) => invoke("sftp_rename", resource, {
        oldPath: safeRemotePath(input, "oldPath", "sftp.rename"),
        newPath: safeRemotePath(input, "newPath", "sftp.rename"),
      }),
    },
    {
      module: "sftp", mode: "write", risk: "dangerous",
      tool: { name: "sftp.delete", description: "Delete a remote file or empty directory.", inputSchema: schema({ path: { type: "string" }, isDir: { type: "boolean" } }, ["path", "isDir"]) },
      approvalPreview: ({ input }) => safeRemotePath(input, "path", "sftp.delete"),
      execute: ({ input, resource }) => invoke("sftp_delete", resource, { path: safeRemotePath(input, "path", "sftp.delete"), isDir: input.isDir === true }),
    },
    {
      module: "sftp", mode: "write", risk: "dangerous",
      tool: { name: "sftp.upload", description: "Upload model-provided text content to a remote file without accessing a local path.", inputSchema: schema({ remotePath: { type: "string" }, content: { type: "string" } }, ["remotePath", "content"]) },
      approvalPreview: ({ input }) => safeRemotePath(input, "remotePath", "sftp.upload"),
      execute: ({ input, resource }) => invoke("sftp_upload_bytes", resource, {
        remotePath: safeRemotePath(input, "remotePath", "sftp.upload"),
        bytes: Array.from(new TextEncoder().encode(content(input, "sftp.upload"))),
      }),
    },
    {
      module: "sftp", mode: "read", risk: "read",
      tool: { name: "sftp.home", description: "Read the remote HOME directory of a connected SFTP session, useful as a starting point for sftp.list.", inputSchema: schema({}) },
      execute: ({ resource }) => invoke("sftp_get_home", resource, {}),
    },
    {
      module: "sftp", mode: "read", risk: "read",
      tool: { name: "sftp.list_transfers", description: "List upload and download jobs of a connected SFTP session with their status and progress.", inputSchema: schema({}) },
      execute: async ({ resource }) => sanitizeAiResult(await invoke("sftp_transfer_list", resource, {})),
    },
    {
      module: "sftp", mode: "write", risk: "write",
      tool: { name: "sftp.cancel_transfer", description: "Cancel a running SFTP transfer job. A partially written file is left on the destination.", inputSchema: schema({ jobId: { type: "string", description: "Job id from sftp.list_transfers." } }, ["jobId"]) },
      approvalPreview: ({ input }) => `Cancel SFTP transfer ${text(input, "jobId")}`,
      execute: ({ input, resource }) => invoke("sftp_transfer_cancel", resource, { jobId: text(input, "jobId") }),
    },
  ];
}
