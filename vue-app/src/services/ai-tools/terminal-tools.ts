import type { AiCapability, AiCapabilityRegistry } from "@/domain/ai/capability-registry";
import type { AiResource, AiResourceRegistry } from "@/domain/ai/resource-registry";
import type { LocalShellType } from "@/domain/terminal/types";
import { sanitizeAiResult } from "./shared";

const LOCAL_SHELL_TYPES: ReadonlySet<LocalShellType> = new Set([
  "system",
  "zsh",
  "bash",
  "fish",
  "pwsh",
  "powershell",
  "cmd",
  "wsl",
]);

export interface CreateTerminalTabInput {
  shellType?: LocalShellType;
  initialCwd?: string;
  title?: string;
  conversationId?: string;
  forceNew?: boolean;
}

export interface TerminalAiDependencies {
  capabilities: AiCapabilityRegistry;
  registry: AiResourceRegistry;
  invoke<T>(command: string, args: Record<string, unknown>): Promise<T>;
  listResources(capability: string): unknown[];
  createTerminalTab?(input: CreateTerminalTabInput): Promise<{ tabId: string; paneId: string }>;
  closeTerminalPane?(sessionId: string): Promise<boolean>;
  /** Shows the pane before the AI operates it (switches to its background terminal tab). */
  revealTerminalPane?(sessionId: string): void;
}

function text(input: Record<string, unknown>, key: string) {
  return typeof input[key] === "string" ? input[key].trim() : "";
}

function shellType(input: Record<string, unknown>): LocalShellType {
  const value = text(input, "shellType");
  if (!value) return "system";
  if (!LOCAL_SHELL_TYPES.has(value as LocalShellType)) {
    throw new Error(`Unsupported local shell type: ${value}`);
  }
  return value as LocalShellType;
}

function sessionId(resource: AiResource) {
  const value = resource.metadata?.sessionId;
  return typeof value === "string" ? value : "";
}

function positiveInteger(input: Record<string, unknown>, key: string) {
  const value = input[key];
  if (typeof value !== "number" || !Number.isFinite(value)) return undefined;
  const rounded = Math.round(value);
  return rounded > 0 ? rounded : undefined;
}

export const terminalCapabilityNames = [
  "terminal.get_status",
  "terminal.resize",
  "terminal.send_input",
  "terminal.close",
];

export function terminalCapabilities(dependencies: TerminalAiDependencies): AiCapability[] {
  return [
    ...(dependencies.createTerminalTab
      ? [{
          module: "terminal",
          discovery: true,
          target: "none" as const,
          mode: "write" as const,
          risk: "write" as const,
          tool: {
            name: "terminal.create_session",
            description: "Create and focus an empty local terminal Tab. Reuses this AI conversation's live terminal unless forceNew is true; never runs an initial command.",
            inputSchema: {
              type: "object",
              properties: {
                shellType: {
                  type: "string",
                  enum: [...LOCAL_SHELL_TYPES],
                  description: "Local shell type; defaults to the system shell.",
                },
                title: { type: "string", description: "Optional Tab title." },
                forceNew: {
                  type: "boolean",
                  description: "Create another terminal instead of reusing this conversation's live terminal.",
                },
              },
              additionalProperties: false,
            },
          },
          approvalPreview: ({ input }: { input: Record<string, unknown> }) =>
            text(input, "title") || (input.forceNew === true ? "新终端" : "当前对话终端"),
          execute: ({
            input,
            conversationId,
          }: {
            input: Record<string, unknown>;
            conversationId?: string;
          }) => dependencies.createTerminalTab!({
            shellType: shellType(input),
            title: text(input, "title") || undefined,
            conversationId,
            forceNew: input.forceNew === true,
          }),
        }]
      : []),
    {
      module: "terminal",
      discovery: true,
      target: "none",
      mode: "write",
      risk: "dangerous",
      tool: {
        name: "terminal.run_command",
        description: "Run one shell command locally and return its stdout, stderr and exit code (default 30s timeout). Use for scripts, builds and verification; prefer this over terminal.create_session + terminal.send_input when you need the command output.",
        inputSchema: {
          type: "object",
          properties: {
            command: { type: "string", description: "Shell command to execute." },
            cwd: { type: "string", description: "Optional absolute working directory." },
            timeoutMs: { type: "integer", minimum: 1, maximum: 120000, description: "Optional timeout in milliseconds; defaults to 30000." },
          },
          required: ["command"],
          additionalProperties: false,
        },
      },
      approvalPreview: ({ input }: { input: Record<string, unknown> }) => text(input, "command") || "命令",
      execute: async ({ input }: { input: Record<string, unknown> }) => {
        const command = text(input, "command");
        if (!command) throw new Error("terminal.run_command requires a command");
        const cwd = text(input, "cwd");
        const timeoutMs = positiveInteger(input, "timeoutMs");
        return sanitizeAiResult(await dependencies.invoke("shell_run_command", {
          payload: {
            command,
            ...(cwd ? { cwd } : {}),
            ...(timeoutMs ? { timeoutMs } : {}),
          },
        }));
      },
    },
    {
      module: "terminal",
      discovery: true,
      target: "none",
      mode: "read",
      risk: "read",
      tool: {
        name: "terminal.list_sessions",
        description: "List live local and SSH terminal sessions.",
        inputSchema: { type: "object", properties: {}, additionalProperties: false },
      },
      execute: () => dependencies.listResources("terminal.send_input"),
    },
    {
      module: "terminal",
      mode: "read",
      risk: "read",
      tool: {
        name: "terminal.get_status",
        description: "Check whether a terminal session is still alive.",
        inputSchema: {
          type: "object",
          properties: {
            resourceId: { type: "string", description: "Stable live terminal resource id." },
            name: { type: "string", description: "Unique terminal name." },
          },
          additionalProperties: false,
        },
      },
      execute: async ({ resource }) => ({
        resourceId: resource.id,
        alive: await dependencies.invoke<boolean>("shell_session_alive", {
          payload: { sessionId: sessionId(resource) },
        }),
      }),
    },
    {
      module: "terminal",
      mode: "write",
      risk: "write",
      tool: {
        name: "terminal.resize",
        description: "Resize a live local terminal PTY.",
        inputSchema: {
          type: "object",
          properties: {
            resourceId: { type: "string", description: "Stable live terminal resource id." },
            name: { type: "string", description: "Unique terminal name." },
            cols: { type: "integer", minimum: 1, maximum: 1_000 },
            rows: { type: "integer", minimum: 1, maximum: 1_000 },
          },
          required: ["cols", "rows"],
          additionalProperties: false,
        },
      },
      approvalPreview: ({ input }) =>
        `${text(input, "resourceId") || text(input, "name") || "当前终端"}: ${String(input.cols)}x${String(input.rows)}`,
      execute: ({ input, resource }) => {
        const id = sessionId(resource);
        if (id) dependencies.revealTerminalPane?.(id);
        return dependencies.invoke("resize_local_shell", {
          payload: { sessionId: id, cols: input.cols, rows: input.rows },
        });
      },
    },
    {
      module: "terminal",
      mode: "write",
      risk: "dangerous",
      tool: {
        name: "terminal.send_input",
        description: "Send text to a live terminal; submit defaults to true and can execute any command.",
        inputSchema: {
          type: "object",
          properties: {
            resourceId: { type: "string", description: "Stable live terminal resource id." },
            name: { type: "string", description: "Unique terminal name." },
            input: { type: "string", description: "Arbitrary terminal input." },
            submit: { type: "boolean", description: "Append Enter; defaults to true." },
          },
          required: ["input"],
          additionalProperties: false,
        },
      },
      approvalPreview: ({ input }) => `${text(input, "resourceId") || text(input, "name") || "当前终端"}: ${text(input, "input")}`,
      execute: async ({ input, resource }) => {
        const id = sessionId(resource);
        const value = text(input, "input");
        if (!id || !value) throw new Error("terminal.send_input requires a live target and input");
        const submitted = input.submit === false || /[\r\n]$/.test(value) ? value : `${value}\r`;
        dependencies.revealTerminalPane?.(id);
        await dependencies.invoke("write_to_shell", { payload: { sessionId: id, input: submitted } });
        return { resourceId: resource.id, accepted: true };
      },
    },
    ...(dependencies.closeTerminalPane
      ? [{
          module: "terminal",
          mode: "write" as const,
          risk: "dangerous" as const,
          tool: {
            name: "terminal.close",
            description: "Close a live terminal pane and its backing shell session.",
            inputSchema: {
              type: "object",
              properties: {
                resourceId: { type: "string", description: "Stable live terminal resource id." },
                name: { type: "string", description: "Unique terminal name." },
              },
              additionalProperties: false,
            },
          },
          approvalPreview: ({ input }: { input: Record<string, unknown> }) =>
            text(input, "resourceId") || text(input, "name") || "当前终端",
          execute: async ({ resource }: { resource: AiResource }) => {
            const closed = await dependencies.closeTerminalPane!(sessionId(resource));
            if (!closed) throw new Error("Terminal pane could not be closed");
            return { resourceId: resource.id, closed: true };
          },
        }]
      : []),
  ];
}
