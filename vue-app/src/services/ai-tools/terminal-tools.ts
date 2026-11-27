import type { AiCapability, AiCapabilityRegistry } from "@/domain/ai/capability-registry";
import type { AiResource, AiResourceRegistry } from "@/domain/ai/resource-registry";
import type { LocalShellType } from "@/domain/terminal/types";
import { useVueI18n } from "@/i18n/messages";
import { aiResourceSummary, sanitizeAiResult, tool } from "./shared";

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
  invoke<T>(command: string, args: Record<string, unknown>, options?: { signal?: AbortSignal }): Promise<T>;
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

function paneControlCapabilities(dependencies: TerminalAiDependencies): AiCapability[] {
  const { t } = useVueI18n();
  const pane = { type: "string", description: "The terminal pane ID returned by the terminal controls." };
  const textProperty = { type: "string" };
  const lines = { type: "integer", minimum: 1, maximum: 5000 };
  const timeoutSecs = { type: "integer", minimum: 1, maximum: 600 };

  function optionalText(input: Record<string, unknown>, key: string) {
    if (input[key] === undefined) return undefined;
    const value = text(input, key);
    if (!value) throw new Error(t("settings.agentControl.invalidText", { field: key }));
    return value;
  }

  function requiredPane(input: Record<string, unknown>) {
    const value = optionalText(input, "pane");
    if (!value) throw new Error(t("settings.agentControl.paneRequired"));
    return value;
  }

  function optionalCount(input: Record<string, unknown>, key: string, maximum: number) {
    const value = input[key];
    if (value === undefined) return undefined;
    if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 1 || value > maximum) {
      throw new Error(t("settings.agentControl.invalidCount", { field: key, maximum }));
    }
    return value;
  }

  return [
    {
      module: "terminal",
      target: "none",
      mode: "write",
      risk: "write",
      tool: tool("terminal.split_pane", "Split a local terminal pane or open a terminal tab when pane is omitted. Optionally run an initial command.", {
        pane, direction: { type: "string", enum: ["right", "down"] },
        cwd: textProperty, title: textProperty, command: textProperty,
      }, ["direction"]),
      approvalPreview: ({ input }) => [text(input, "pane"), text(input, "direction"), text(input, "cwd"), text(input, "command")]
        .filter(Boolean).join(" · "),
      execute: ({ input, projectRoot, signal }) => {
        const { direction } = input;
        if (direction !== "right" && direction !== "down") throw new Error(t("settings.agentControl.paneDirection"));
        const pane = optionalText(input, "pane");
        const cwd = optionalText(input, "cwd") ?? (pane ? undefined : projectRoot);
        const title = optionalText(input, "title");
        const command = optionalText(input, "command");
        return dependencies.invoke("terminal_control_split", {
          direction, ...(pane ? { pane } : {}), ...(cwd ? { cwd } : {}),
          ...(title ? { title } : {}), ...(command ? { command } : {}),
        }, { signal });
      },
    },
    {
      module: "terminal",
      target: "none",
      mode: "read",
      risk: "read",
      tool: tool("terminal.read_output", "Read recent plain-text output from a local terminal pane.", { pane, lines }, ["pane"]),
      execute: async ({ input, signal }) => {
        const pane = requiredPane(input);
        const count = optionalCount(input, "lines", lines.maximum);
        return sanitizeAiResult(await dependencies.invoke("terminal_control_read", {
          pane, ...(count === undefined ? {} : { lines: count }),
        }, { signal }));
      },
    },
    {
      module: "terminal",
      target: "none",
      mode: "read",
      risk: "read",
      tool: tool("terminal.wait_idle", "Wait for a local terminal pane to settle. Returns idle, waiting_input, exited or timeout.", { pane, timeoutSecs }, ["pane"]),
      execute: ({ input, signal }) => {
        const pane = requiredPane(input);
        const seconds = optionalCount(input, "timeoutSecs", timeoutSecs.maximum);
        return dependencies.invoke("terminal_control_wait", {
          pane, ...(seconds === undefined ? {} : { timeoutSecs: seconds }),
        }, { signal });
      },
    },
    {
      module: "terminal",
      target: "none",
      mode: "read",
      risk: "read",
      tool: tool("terminal.pane_info", "Get the title, directory, liveness and activity state of a local terminal pane.", { pane }, ["pane"]),
      execute: ({ input, signal }) => dependencies.invoke("terminal_control_info", { pane: requiredPane(input) }, { signal }),
    },
  ];
}

export function terminalCapabilities(dependencies: TerminalAiDependencies): AiCapability[] {
  return [
    ...paneControlCapabilities(dependencies),
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
            projectRoot,
          }: {
            input: Record<string, unknown>;
            conversationId?: string;
            projectRoot?: string;
          }) => dependencies.createTerminalTab!({
            shellType: shellType(input),
            initialCwd: projectRoot,
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
      execute: async ({ input, externalAgent, signal }) => {
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
        }, externalAgent ? { signal } : undefined));
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
      execute: ({ externalAgent, resources }) =>
        (externalAgent ? resources?.list?.("terminal.send_input")?.map(aiResourceSummary) : undefined)
        ?? dependencies.listResources("terminal.send_input"),
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
