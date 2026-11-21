import type {
  AiCapability,
  AiCapabilityRegistry,
} from "@/domain/ai/capability-registry";
import type { AiResourceRegistry } from "@/domain/ai/resource-registry";
import type { SshRuntimeCredentials } from "@/domain/ssh/types";
import { gitCapabilities, gitCapabilityNames } from "./ai-tools/git-tools";
import {
  databaseCapabilities,
  databaseSavedCapabilityNames,
  databaseSessionCapabilityNames,
} from "./ai-tools/database-tools";
import {
  redisCapabilities,
  redisSavedCapabilityNames,
  redisSessionCapabilityNames,
} from "./ai-tools/redis-tools";
import {
  workspaceCapabilities,
  workspaceCapabilityNames,
} from "./ai-tools/workspace-tools";
import { httpCapabilities } from "./ai-tools/http-tools";
import {
  browserCapabilities,
  browserCapabilityNames,
} from "./ai-tools/browser-tools";
import { computerCapabilities } from "./ai-tools/computer-tools";
import {
  terminalCapabilities,
  terminalCapabilityNames,
  type CreateTerminalTabInput,
} from "./ai-tools/terminal-tools";
import {
  sshCapabilities,
  sshProfileCapabilityNames,
  sshSessionCapabilityNames,
} from "./ai-tools/ssh-tools";
import {
  sftpCapabilities,
  sftpCapabilityNames,
  sftpProfileCapabilityNames,
  sftpWorkspaceTransferCapabilities,
  sftpWorkspaceTransferCapabilityNames,
} from "./ai-tools/sftp-tools";
import {
  settingsCapabilities,
  type SafeSettingsUpdate,
} from "./ai-tools/settings-tools";
import { systemCapabilities } from "./ai-tools/system-tools";
import { devtoolsCapabilities } from "./ai-tools/devtools-tools";
import { pluginCapabilities } from "./ai-tools/plugin-tools";
import { sessionsCapabilities } from "./ai-tools/sessions-tools";
import { subagentCapabilities } from "./ai-tools/subagent-tools";
import type { AiSqlSafety } from "./ai-tools/sql-safety";
import { runWithSafety, sqlSafetyArgs } from "./sql-safety-service";
import { confirmSqlSafety } from "./sql-safety-confirm";

/** AI 数据库 / Redis 写工具的 SQL 安全网：宿主确认框由用户作答，任何审批模式都会弹出。 */
const aiSqlSafety: AiSqlSafety = { runWithSafety, sqlSafetyArgs, confirm: confirmSqlSafety };

export interface NativeAiTerminalPane {
  id: string;
  type: "local" | "ssh";
  name: string;
  profileId?: string;
  connected?: boolean;
}

export interface NativeAiSshProfile {
  id: string;
  name: string;
  host: string;
  port: number;
  username: string;
  authType: string;
  keyFile?: string;
  jumpHost?: unknown;
  socksProxy?: { host: string; port: number; username?: string };
}

export interface NativeAiSftpSession {
  id: string;
  name: string;
  profileId: string;
  connected: boolean;
}

export interface NativeAiResourceSnapshot {
  nativeRuntime?: boolean;
  projectRoot?: string;
  gitRepositories?: Array<{ path: string; name?: string }>;
  activeGitRepositoryPath?: string;
  activeTerminalId?: string;
  database?: NativeAiDatabaseSnapshot;
  embeddedBrowser?: {
    available: boolean;
    shared: boolean;
    tabId?: string;
    url?: string;
  };
  terminalPanes: NativeAiTerminalPane[];
  sshProfiles: NativeAiSshProfile[];
  sftpSessions?: NativeAiSftpSession[];
}

interface NativeAiToolDependencies {
  registry: AiResourceRegistry;
  invoke<T>(command: string, args: Record<string, unknown>): Promise<T>;
  sshProfile?(profileId: string): NativeAiSshProfile | undefined;
  sshCredentials?(profileId: string): SshRuntimeCredentials | undefined;
  runSubagentPlan?(
    plan: import("@/domain/ai/subagent-orchestration").SubagentPlanDefinition,
    context: { conversationId?: string; signal: AbortSignal },
  ): Promise<unknown>;
  integrateSubagentResult?(
    result: { branch: string; commitHash: string; baseCommit: string },
    context: { conversationId?: string; signal: AbortSignal },
  ): Promise<unknown>;
}

export interface NativeAiDatabaseSnapshot {
  connections: Array<Record<string, unknown> & { id: string; name: string }>;
  sessions: Record<string, string>;
  schemas: Record<string, unknown>;
  activeConnectionId?: string;
}

export interface NativeAiCapabilityDependencies
  extends NativeAiToolDependencies {
  nativeRuntime?: boolean;
  capabilities: AiCapabilityRegistry;
  listResources(capability: string): unknown[];
  createTerminalTab?(
    input: CreateTerminalTabInput,
  ): Promise<{ tabId: string; paneId: string }>;
  openBrowserTab?(input: { url?: string }): Promise<unknown>;
  browserTabs?: import("@/services/ai-tools/shared").BrowserTabsDependency;
  closeTerminalPane?(sessionId: string): Promise<boolean>;
  revealTerminalPane?(sessionId: string): void;
  connectSftp?(profileId: string): Promise<{ sessionId: string; home: string }>;
  disconnectSftp?(sessionId: string): Promise<void>;
  workspaceRoot?(): string;
  openWorkspaceView?(input: {
    view: import("@/types/workspace").WorkspaceView;
  }): Promise<unknown>;
  projectAction?(input: Record<string, unknown>): Promise<unknown>;
  listSavedProjects?(conversationId?: string): Array<{ root: string; name: string }>;
  httpAction?(input: Record<string, unknown>): Promise<unknown>;
  databaseAction?(input: Record<string, unknown>): Promise<unknown>;
  redisAction?(input: Record<string, unknown>): Promise<unknown>;
  databaseSnapshot?(): NativeAiDatabaseSnapshot;
  connectDatabase?(connectionId: string): Promise<unknown>;
  disconnectDatabase?(connectionId: string): Promise<unknown>;
  /** 插件驱动连接的审批预览标签（E-11）；内置驱动返回 undefined。 */
  databaseDriverLabel?(connectionId: string): string | undefined;
  settingsSummary?(): Record<string, unknown>;
  updateSafeSettings?(update: SafeSettingsUpdate): Promise<unknown>;
  sidebarVisibility?(): Promise<
    import("@/services/sidebar-preferences").SidebarVisibility
  >;
  setSidebarItemVisible?(
    id: import("@/services/sidebar-preferences").SidebarItemId,
    visible: boolean,
  ): Promise<unknown>;
  listSavedMcpServers?(): Promise<Array<{ id: string; connected: boolean }>>;
  connectSavedMcpServer?(serverId: string): Promise<unknown>;
  stageWorkspaceEdit?(input: {
    path: string;
    projectRoot: string;
    before: string | null;
    after: string;
  }): unknown;
  sshCredentials?(profileId: string): SshRuntimeCredentials | undefined;
}

function stringArgument(args: Record<string, unknown>, key: string) {
  return typeof args[key] === "string" ? args[key].trim() : "";
}

function resolveOrThrow(
  registry: AiResourceRegistry,
  args: Record<string, unknown>,
  capability: string,
) {
  const resolution = registry.resolve({
    resourceId: stringArgument(args, "resourceId") || undefined,
    name: stringArgument(args, "name") || undefined,
    capability,
  });
  if (resolution.status === "resolved") return resolution.resource;
  if (resolution.status === "ambiguous") {
    throw new Error(
      `目标不唯一: ${resolution.candidates.map((candidate) => candidate.name).join(", ")}`,
    );
  }
  throw new Error(`未找到支持 ${capability} 的目标资源`);
}

function canonicalProjectRoot(projectRoot: string) {
  const normalized = projectRoot.trim().replace(/\\/g, "/");
  if (!normalized) return "";
  const drive = normalized.match(/^[A-Za-z]:/)?.[0] ?? "";
  const absolute = normalized.startsWith("/") || Boolean(drive);
  const segments: string[] = [];
  for (const segment of normalized.slice(drive.length).split("/")) {
    if (!segment || segment === ".") continue;
    if (segment === "..") {
      if (segments.length && segments[segments.length - 1] !== "..")
        segments.pop();
      else if (!absolute) segments.push(segment);
      continue;
    }
    segments.push(segment);
  }
  const prefix = drive ? `${drive}/` : absolute ? "/" : "";
  return `${prefix}${segments.join("/")}` || (absolute ? prefix : ".");
}

function stableProjectIdentity(projectRoot: string) {
  let first = 0x811c9dc5;
  let second = 0x9e3779b9;
  for (let index = 0; index < projectRoot.length; index += 1) {
    const code = projectRoot.charCodeAt(index);
    first = Math.imul(first ^ code, 0x01000193);
    second = Math.imul(second ^ code, 0x85ebca6b);
  }
  return [first, second]
    .map((value) => (value >>> 0).toString(16).padStart(8, "0"))
    .join("");
}

export type NativeAiResourceSource =
  | "workspace"
  | "git"
  | "database"
  | "redis"
  | "browser"
  | "terminal"
  | "ssh-profile"
  | "sftp"
  | "sftp-workspace-transfer";

/**
 * 资源注册表是全局单例，registry.replace 会整片覆盖一个来源。
 * 只掌握部分状态的调用方（如自动化中心）用 sources 限定自己负责的来源，
 * 避免把别处刚注册的终端、SSH 等资源清空；不传则同步全部来源。
 */
export function syncNativeAiResources(
  registry: AiResourceRegistry,
  snapshot: NativeAiResourceSnapshot,
  sources?: readonly NativeAiResourceSource[],
) {
  const syncs = (source: NativeAiResourceSource) =>
    !sources || sources.includes(source);
  const projectRoot = canonicalProjectRoot(snapshot.projectRoot ?? "");
  const activeGitRepositoryPath = canonicalProjectRoot(
    snapshot.activeGitRepositoryPath ?? "",
  );
  if (syncs("workspace"))
    registry.replace(
      "workspace",
      projectRoot
        ? [
            {
              id: `workspace-project:${stableProjectIdentity(projectRoot)}`,
              kind: "workspace-project",
              name: projectRoot.split("/").filter(Boolean).pop() ?? "workspace",
              state: "available" as const,
              capabilities: workspaceCapabilityNames,
              metadata: { projectRoot },
            },
          ]
        : [],
    );
  if (syncs("git"))
    registry.replace(
      "git",
      (snapshot.gitRepositories ?? []).flatMap((repository) => {
        const repoPath = canonicalProjectRoot(repository.path);
        if (!repoPath) return [];
        return [
          {
            id: `git-repository:${stableProjectIdentity(repoPath)}`,
            kind: "git-repository",
            name:
              repository.name?.trim() ||
              repoPath.split("/").filter(Boolean).pop() ||
              "repository",
            state: "available" as const,
            active: repoPath === activeGitRepositoryPath,
            capabilities: gitCapabilityNames,
            metadata: { projectRoot, repoPath },
          },
        ];
      }),
    );
  const databaseSnapshot = snapshot.database;
  const safeConnections = (databaseSnapshot?.connections ?? []).flatMap(
    (connection) => {
      const dbType =
        typeof connection.dbType === "string" ? connection.dbType : "";
      if (!dbType) return [];
      const sessionId = databaseSnapshot?.sessions[connection.id];
      const module = dbType === "redis" ? "redis" : "database";
      const savedCapabilities =
        module === "redis"
          ? redisSavedCapabilityNames
          : databaseSavedCapabilityNames;
      const sessionCapabilities =
        module === "redis"
          ? redisSessionCapabilityNames
          : databaseSessionCapabilityNames;
      return [
        {
          id: `${module}:${connection.id}`,
          kind: `${module}-${sessionId ? "session" : "connection"}`,
          name: connection.name,
          state: "available" as const,
          active: connection.id === databaseSnapshot?.activeConnectionId,
          capabilities: sessionId ? sessionCapabilities : savedCapabilities,
          metadata: {
            connectionId: connection.id,
            dbType,
            // 非敏感的驱动标注：AI 只能看到绑定了哪个插件驱动，不能修改或授予。
            ...(connection.driver &&
            typeof connection.driver === "object" &&
            typeof (connection.driver as { pluginId?: unknown }).pluginId === "string"
              ? {
                  pluginDriver: {
                    pluginId: (connection.driver as { pluginId: string }).pluginId,
                    driverId: String(
                      (connection.driver as { driverId?: unknown }).driverId ?? "",
                    ),
                  },
                }
              : {}),
            ...(sessionId
              ? { sessionId, schema: databaseSnapshot?.schemas[connection.id] }
              : {}),
          },
        },
      ];
    },
  );
  if (syncs("database"))
    registry.replace(
      "database",
      safeConnections.filter(
        (resource) => resource.metadata.dbType !== "redis",
      ),
    );
  if (syncs("redis"))
    registry.replace(
      "redis",
      safeConnections.filter(
        (resource) => resource.metadata.dbType === "redis",
      ),
    );
  const embeddedBrowser = snapshot.embeddedBrowser;
  if (syncs("browser"))
    registry.replace(
      "browser",
      embeddedBrowser?.available &&
        embeddedBrowser.shared &&
        embeddedBrowser.tabId
        ? [
            {
              id: "browser:active",
              kind: "embedded-browser",
              name: "Shared browser tab",
              state: "available" as const,
              active: true,
              capabilities: browserCapabilityNames,
              metadata: {
                tabId: embeddedBrowser.tabId,
                url: embeddedBrowser.url ?? "",
              },
            },
          ]
        : [],
    );
  const nativeRuntime = snapshot.nativeRuntime === true;
  const liveTerminalPanes = nativeRuntime
    ? snapshot.terminalPanes.filter((pane) => pane.connected !== false)
    : [];
  if (syncs("terminal"))
    registry.replace(
      "terminal",
      liveTerminalPanes.map((pane) => ({
        id: `terminal-session:${pane.id}`,
        kind: pane.type === "ssh" ? "ssh-shell" : "terminal-session",
        name: pane.name,
        state: "available" as const,
        active: pane.id === snapshot.activeTerminalId,
        capabilities:
          pane.type === "ssh"
            ? [
                ...terminalCapabilityNames.filter(
                  (name) => name !== "terminal.resize",
                ),
                ...sshSessionCapabilityNames,
              ]
            : terminalCapabilityNames,
        metadata: {
          sessionId: pane.id,
          profileId: pane.profileId,
          type: pane.type,
        },
      })),
    );
  if (syncs("ssh-profile"))
    registry.replace(
      "ssh-profile",
      nativeRuntime
        ? snapshot.sshProfiles.map((profile) => ({
            id: `ssh-profile:${profile.id}`,
            kind: "ssh-profile",
            name: profile.name,
            state: "available" as const,
            capabilities: [
              ...sshProfileCapabilityNames,
              ...sftpProfileCapabilityNames,
            ],
            metadata: { profileId: profile.id },
          }))
        : [],
    );
  const connectedSftpSessions = (
    nativeRuntime ? (snapshot.sftpSessions ?? []) : []
  ).filter((session) => session.connected);
  if (syncs("sftp"))
    registry.replace(
      "sftp",
      connectedSftpSessions.map((session) => ({
        id: `sftp-session:${session.id}`,
        kind: "sftp-session",
        name: session.name,
        state: "available" as const,
        capabilities: sftpCapabilityNames,
        metadata: { sessionId: session.id, profileId: session.profileId },
      })),
    );
  if (syncs("sftp-workspace-transfer"))
    registry.replace(
      "sftp-workspace-transfer",
      projectRoot
        ? connectedSftpSessions.map((session) => ({
            id: `sftp-workspace-transfer:${stableProjectIdentity(projectRoot)}:${session.id}`,
            kind: "sftp-workspace-transfer",
            name: `${projectRoot.split("/").filter(Boolean).pop() ?? "workspace"} ↔ ${session.name}`,
            state: "available" as const,
            capabilities: sftpWorkspaceTransferCapabilityNames,
            metadata: {
              projectRoot,
              sessionId: session.id,
              profileId: session.profileId,
            },
          }))
        : [],
    );
  if (syncs("terminal"))
    registry.setActive(
      snapshot.activeTerminalId
        ? `terminal-session:${snapshot.activeTerminalId}`
        : undefined,
    );
}

function globalCapabilities(
  dependencies: NativeAiCapabilityDependencies,
): AiCapability[] {
  const global = (capability: Omit<AiCapability, "target">): AiCapability => ({
    ...capability,
    target: "none",
  });
  const tool = (
    name: string,
    description: string,
    properties: Record<string, unknown> = {},
    required: string[] = [],
  ) => ({
    name,
    description,
    inputSchema: {
      type: "object",
      properties,
      ...(required.length ? { required } : {}),
      additionalProperties: false,
    },
  });

  return [
    ...workspaceCapabilities(dependencies),
    ...(dependencies.runSubagentPlan && dependencies.integrateSubagentResult
      ? subagentCapabilities({
          runPlan: dependencies.runSubagentPlan,
          integrateResult: dependencies.integrateSubagentResult,
        })
      : []),
    ...(["database", "redis"] as const).map((module) =>
      global({
        module,
        tool: tool(
          `${module}.list_connections`,
          `List saved ${module} connections without secrets.`,
        ),
        discovery: true,
        mode: "read",
        risk: "read",
        execute: () => {
          const snapshot = dependencies.databaseSnapshot?.();
          return (snapshot?.connections ?? [])
            .filter(
              (connection) =>
                (connection.dbType === "redis") === (module === "redis"),
            )
            .map((connection) => ({
              id: connection.id,
              name: connection.name,
              dbType: connection.dbType,
              host: connection.host,
              port: connection.port,
              database: connection.database,
              connected: Boolean(snapshot?.sessions[connection.id]),
            }));
        },
      }),
    ),
    ...databaseCapabilities({
      ...dependencies,
      databaseSessionId: (connectionId) =>
        dependencies.databaseSnapshot?.().sessions[connectionId],
      sqlSafety: aiSqlSafety,
    }),
    ...redisCapabilities({ ...dependencies, sqlSafety: aiSqlSafety }),
    ...gitCapabilities(dependencies),
    ...(dependencies.nativeRuntime === true
      ? httpCapabilities(dependencies)
      : []),
    ...browserCapabilities(dependencies),
    ...(dependencies.nativeRuntime === true
      ? computerCapabilities(dependencies)
      : []),
    ...devtoolsCapabilities(),
    ...pluginCapabilities(dependencies, dependencies.nativeRuntime),
    ...sessionsCapabilities(dependencies, dependencies.nativeRuntime),
    ...settingsCapabilities(dependencies),
    ...(dependencies.nativeRuntime === true
      ? systemCapabilities(dependencies)
      : []),
  ];
}

export function registerNativeAiCapabilities(
  dependencies: NativeAiCapabilityDependencies,
) {
  const nativeCapabilities =
    dependencies.nativeRuntime === true
      ? [
          ...terminalCapabilities(dependencies),
          ...sshCapabilities(dependencies),
          ...sftpCapabilities(dependencies),
          ...sftpWorkspaceTransferCapabilities(dependencies),
        ]
      : [];
  const registrations = [
    ...globalCapabilities(dependencies),
    ...nativeCapabilities,
  ].map((capability) => dependencies.capabilities.register(capability));
  return () => registrations.forEach((unregister) => unregister());
}

export async function executeNativeAiTool(
  toolName: string,
  args: Record<string, unknown>,
  dependencies: NativeAiToolDependencies,
): Promise<unknown> {
  if (toolName === "terminal.send_input") {
    const resource = resolveOrThrow(
      dependencies.registry,
      args,
      "terminal.send_input",
    );
    const sessionId =
      typeof resource.metadata?.sessionId === "string"
        ? resource.metadata.sessionId
        : "";
    const input = stringArgument(args, "input");
    if (!sessionId || !input)
      throw new Error("terminal.send_input requires a target and input");
    const submittedInput =
      args.submit === false || /[\r\n]$/.test(input) ? input : `${input}\r`;
    await dependencies.invoke("write_to_shell", {
      payload: { sessionId, input: submittedInput },
    });
    return { resourceId: resource.id, accepted: true };
  }

  if (toolName === "ssh.run_command") {
    const resource = resolveOrThrow(
      dependencies.registry,
      args,
      "ssh.run_command",
    );
    const profileId =
      typeof resource.metadata?.profileId === "string"
        ? resource.metadata.profileId
        : "";
    const command = stringArgument(args, "command");
    const profile = dependencies.sshProfile?.(profileId);
    const credentials = dependencies.sshCredentials?.(profileId);
    if (!profileId || !profile || !command) {
      throw new Error("ssh.run_command requires a target and command");
    }
    return dependencies.invoke("ssh_batch_exec", {
      payload: {
        servers: [
          {
            id: profile.id,
            host: profile.host,
            port: profile.port,
            username: profile.username,
            authType: profile.authType,
            password: credentials?.password ?? null,
            keyFile: profile.keyFile ?? null,
            passphrase: credentials?.passphrase ?? null,
            totpUri: credentials?.totpUri ?? null,
            jumpHost: profile.jumpHost
              ? {
                  ...(profile.jumpHost as Record<string, unknown>),
                  password: credentials?.jumpPassword ?? null,
                  passphrase: credentials?.jumpPassphrase ?? null,
                }
              : null,
            socksProxy: profile.socksProxy
              ? {
                  host: profile.socksProxy.host,
                  port: profile.socksProxy.port,
                  username: profile.socksProxy.username ?? null,
                  password: credentials?.socksPassword ?? null,
                }
              : null,
          },
        ],
        command,
      },
    });
  }

  throw new Error(`Unknown native AI tool: ${toolName}`);
}
