import type { AiCapability } from "@/domain/ai/capability-registry";
import type { SidebarItemId, SidebarVisibility } from "@/services/sidebar-preferences";
import type { UiTheme } from "@/services/ui-theme";
import { clearLocalePackSelection } from "@/services/locale-pack-loader";
import { callMcpToolCancellable } from "@/services/mcp-tool-call";
import { requiredString, sanitizeAiResult, tool, type AiToolDependencies } from "./shared";

export interface SafeSettingsUpdate {
  theme?: UiTheme;
  locale?: "zh" | "en";
  minimizeToTray?: boolean;
  autostart?: boolean;
}

export interface SettingsToolDependencies extends AiToolDependencies {
  nativeRuntime?: boolean;
  settingsSummary?(): Record<string, unknown>;
  updateSafeSettings?(update: SafeSettingsUpdate): Promise<unknown>;
  sidebarVisibility?(): Promise<SidebarVisibility>;
  setSidebarItemVisible?(id: SidebarItemId, visible: boolean): Promise<unknown>;
  listSavedMcpServers?(): Promise<Array<{ id: string; connected: boolean }>>;
  connectSavedMcpServer?(serverId: string): Promise<unknown>;
}

function count(value: unknown) {
  return typeof value === "number" && Number.isFinite(value) && value >= 0
    ? Math.floor(value)
    : 0;
}

function safeSettingsSummary(value: Record<string, unknown>) {
  return {
    activeAiProfileConfigured:
      typeof value.activeAiProfileId === "string" && Boolean(value.activeAiProfileId.trim()),
    acpAdapterConfigured:
      typeof value.acpAdapterId === "string" && Boolean(value.acpAdapterId.trim()),
    acpInjectRules: value.acpInjectRules === true,
    enabledMcpServers: count(value.enabledMcpServers),
    enabledAiSkills: count(value.enabledAiSkills),
    enabledAiRules: count(value.enabledAiRules),
  };
}

function safeSettingsUpdate(input: Record<string, unknown>): SafeSettingsUpdate {
  const update: SafeSettingsUpdate = {};
  if (["light", "dark", "graphite", "midnight", "paper", "cloud-blue", "arco", "arco-dark"].includes(String(input.theme))) {
    update.theme = input.theme as UiTheme;
  }
  if (input.locale === "zh" || input.locale === "en") update.locale = input.locale;
  if (typeof input.minimizeToTray === "boolean") update.minimizeToTray = input.minimizeToTray;
  if (typeof input.autostart === "boolean") update.autostart = input.autostart;
  return update;
}

function settingsUpdatePreview(input: Record<string, unknown>) {
  const update = safeSettingsUpdate(input);
  const entries = Object.entries(update);
  return entries.length
    ? `Update application settings: ${entries.map(([key, value]) => `${key}=${JSON.stringify(value)}`).join(", ")}`
    : "Update application settings: no allowlisted values";
}

function safeMcpStatuses(value: unknown) {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item) => {
    if (!item || typeof item !== "object" || Array.isArray(item)) return [];
    const status = item as Record<string, unknown>;
    if (typeof status.id !== "string" || typeof status.state !== "string") return [];
    return [{
      id: status.id,
      state: status.state,
      toolCount: count(status.toolCount),
      resourceCount: count(status.resourceCount),
      promptCount: count(status.promptCount),
      hasError: typeof status.error === "string" && Boolean(status.error),
    }];
  });
}

export function settingsCapabilities(
  dependencies: SettingsToolDependencies
): AiCapability[] {
  return [
    {
      module: "settings",
      target: "none",
      discovery: true,
      tool: tool("settings.get_summary", "Read an allowlisted non-secret application settings summary."),
      mode: "read",
      risk: "read",
      execute: () => safeSettingsSummary(dependencies.settingsSummary?.() ?? {}),
    },
    ...(dependencies.updateSafeSettings ? [{
      module: "settings",
      target: "none" as const,
      tool: tool("settings.update_ui", "Update allowlisted non-sensitive UI and desktop behavior settings.", {
        theme: { type: "string", enum: ["light", "dark", "graphite", "midnight", "paper", "cloud-blue", "arco", "arco-dark"] },
        locale: { type: "string", enum: ["zh", "en"] },
        minimizeToTray: { type: "boolean" },
        autostart: { type: "boolean" },
      }),
      mode: "write" as const,
      risk: "write" as const,
      approvalPreview: ({ input }: { input: Record<string, unknown> }) => settingsUpdatePreview(input),
      execute: ({ input }: { input: Record<string, unknown> }) => {
        const update = safeSettingsUpdate(input);
        if (!Object.keys(update).length) throw new Error("settings.update_ui requires at least one setting");
        // AI may switch the base language (zh | en) but never selects or keeps a language pack.
        if (update.locale !== undefined) clearLocalePackSelection({ persist: true });
        return dependencies.updateSafeSettings?.(update);
      },
    }] : []),
    ...(dependencies.sidebarVisibility && dependencies.setSidebarItemVisible ? [{
      module: "settings",
      target: "none" as const,
      tool: tool("settings.get_sidebar", "Read sidebar item visibility."),
      mode: "read" as const,
      risk: "read" as const,
      execute: () => dependencies.sidebarVisibility?.(),
    }, {
      module: "settings",
      target: "none" as const,
      tool: tool("settings.set_sidebar_item", "Show or hide one allowlisted sidebar item.", {
        itemId: { type: "string" },
        visible: { type: "boolean" },
      }, ["itemId", "visible"]),
      mode: "write" as const,
      risk: "write" as const,
      approvalPreview: ({ input }: { input: Record<string, unknown> }) =>
        `${input.visible === true ? "Show" : "Hide"} sidebar item ${requiredString(input, "itemId", "settings.set_sidebar_item")}`,
      execute: ({ input }: { input: Record<string, unknown> }) => dependencies.setSidebarItemVisible?.(
        requiredString(input, "itemId", "settings.set_sidebar_item") as SidebarItemId,
        input.visible === true
      ),
    }] : []),
    ...(dependencies.nativeRuntime === false ? [] : [{
      module: "settings",
      target: "none" as const,
      tool: tool("settings.get_app_version", "Read the application version."),
      mode: "read" as const,
      risk: "read" as const,
      execute: () => dependencies.invoke("get_app_version", {}),
    }, ...(dependencies.listSavedMcpServers ? [{
      module: "settings",
      target: "none" as const,
      tool: tool("settings.list_saved_mcp_servers", "List saved and enabled connector identifiers without connection commands, URLs, headers or environment variables."),
      mode: "read" as const,
      risk: "read" as const,
      execute: () => dependencies.listSavedMcpServers?.() ?? Promise.resolve([]),
    }] : []), ...(dependencies.connectSavedMcpServer ? [{
      module: "settings",
      target: "none" as const,
      tool: tool("settings.connect_saved_mcp_server", "Connect one saved and enabled connector by identifier. Its local command or remote connection settings remain hidden from AI.", {
        serverId: { type: "string", description: "Identifier from settings.list_saved_mcp_servers." },
      }, ["serverId"]),
      mode: "write" as const,
      risk: "dangerous" as const,
      approvalPreview: ({ input }: { input: Record<string, unknown> }) =>
        `Connect saved MCP server ${requiredString(input, "serverId", "settings.connect_saved_mcp_server")}`,
      execute: ({ input }: { input: Record<string, unknown> }) => dependencies.connectSavedMcpServer?.(
        requiredString(input, "serverId", "settings.connect_saved_mcp_server")
      ),
    }] : []), {
      module: "settings",
      target: "none" as const,
      tool: tool("settings.list_mcp_status", "List MCP connection status and capability counts without connection secrets."),
      mode: "read" as const,
      risk: "read" as const,
      execute: async () => safeMcpStatuses(await dependencies.invoke("mcp_list_status", {})),
    }, {
      module: "settings",
      target: "none" as const,
      tool: tool("settings.list_mcp_tools", "List tools exposed by an already connected MCP server.", {
        serverId: { type: "string" },
      }, ["serverId"]),
      mode: "read" as const,
      risk: "read" as const,
      execute: ({ input }: { input: Record<string, unknown> }) => dependencies.invoke("mcp_list_tools", {
        serverId: requiredString(input, "serverId", "settings.list_mcp_tools"),
      }),
    }, {
      module: "settings",
      target: "none" as const,
      tool: tool("settings.list_mcp_resources", "List resources exposed by an already connected MCP server.", {
        serverId: { type: "string" },
      }, ["serverId"]),
      mode: "read" as const,
      risk: "read" as const,
      execute: ({ input }: { input: Record<string, unknown> }) => dependencies.invoke("mcp_list_resources", {
        serverId: requiredString(input, "serverId", "settings.list_mcp_resources"),
      }),
    }, {
      module: "settings",
      target: "none" as const,
      tool: tool("settings.read_mcp_resource", "Read the contents of one resource from a connected MCP server.", {
        serverId: { type: "string" },
        uri: { type: "string", description: "Resource uri from settings.list_mcp_resources." },
      }, ["serverId", "uri"]),
      mode: "read" as const,
      risk: "network" as const,
      execute: async ({ input }: { input: Record<string, unknown> }) => sanitizeAiResult(await dependencies.invoke("mcp_read_resource", {
        serverId: requiredString(input, "serverId", "settings.read_mcp_resource"),
        uri: requiredString(input, "uri", "settings.read_mcp_resource"),
      })),
    }, {
      module: "settings",
      target: "none" as const,
      tool: tool("settings.list_mcp_prompts", "List prompt templates exposed by an already connected MCP server.", {
        serverId: { type: "string" },
      }, ["serverId"]),
      mode: "read" as const,
      risk: "read" as const,
      execute: ({ input }: { input: Record<string, unknown> }) => dependencies.invoke("mcp_list_prompts", {
        serverId: requiredString(input, "serverId", "settings.list_mcp_prompts"),
      }),
    }, {
      module: "settings",
      target: "none" as const,
      tool: tool("settings.get_mcp_prompt", "Render one prompt template from a connected MCP server.", {
        serverId: { type: "string" },
        name: { type: "string", description: "Prompt name from settings.list_mcp_prompts." },
        arguments: { type: "object", description: "Template arguments; omit when the prompt takes none." },
      }, ["serverId", "name"]),
      mode: "read" as const,
      risk: "network" as const,
      execute: async ({ input }: { input: Record<string, unknown> }) => sanitizeAiResult(await dependencies.invoke("mcp_get_prompt", {
        serverId: requiredString(input, "serverId", "settings.get_mcp_prompt"),
        name: requiredString(input, "name", "settings.get_mcp_prompt"),
        arguments: input.arguments && typeof input.arguments === "object" ? input.arguments : {},
      })),
    }, {
      module: "settings",
      target: "none" as const,
      /**
       * MCP 工具是外部服务器定义的，这里没法预知它会做什么，
       * 所以一律当危险操作走审批，不按 serverId 放行。
       */
      tool: tool("settings.call_mcp_tool", "Call one tool on a connected MCP server. The tool is defined by that external server, so its effects are outside this app's control.", {
        serverId: { type: "string" },
        toolName: { type: "string", description: "Tool name from settings.list_mcp_tools." },
        arguments: { type: "object", description: "Arguments matching the tool's inputSchema." },
      }, ["serverId", "toolName"]),
      mode: "write" as const,
      risk: "dangerous" as const,
      approvalPreview: ({ input }: { input: Record<string, unknown> }) =>
        `${requiredString(input, "serverId", "settings.call_mcp_tool")}: ${requiredString(input, "toolName", "settings.call_mcp_tool")}`,
      execute: async ({ input, signal }: { input: Record<string, unknown>; signal: AbortSignal }) => sanitizeAiResult(await callMcpToolCancellable(
        (command, args) => dependencies.invoke(command, args),
        {
          serverId: requiredString(input, "serverId", "settings.call_mcp_tool"),
          toolName: requiredString(input, "toolName", "settings.call_mcp_tool"),
          arguments: input.arguments && typeof input.arguments === "object" ? input.arguments : {},
        },
        signal,
      )),
    }]),
  ];
}
