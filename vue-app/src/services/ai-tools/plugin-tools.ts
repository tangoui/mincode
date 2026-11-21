import type { AiCapability, AiCapabilityRisk } from "@/domain/ai/capability-registry";
import { requiredString, sanitizeAiResult, tool, type AiToolDependencies } from "./shared";

const PLUGIN_ACTION_RISKS = new Set<string>(["read", "network", "write", "dangerous"]);

export function pluginCapabilities(dependencies: AiToolDependencies, nativeRuntime?: boolean): AiCapability[] {
  if (!nativeRuntime) return [];
  return [
    { module: "plugin", target: "none", discovery: true, tool: tool("plugin.describe", "Describe plugin actions, parameter schemas, risks and examples. Only aiCallable actions can be invoked.", { id: { type: "string" } }, ["id"]), mode: "read", risk: "read", execute: async ({ input }) => sanitizeAiResult(await dependencies.invoke("plugin_host_describe", { id: requiredString(input, "id", "plugin.describe") })) },
    { module: "plugin", target: "none", discovery: true, tool: tool("plugin.list", "List built-in plugins and enabled state without plugin configuration secrets."), mode: "read", risk: "read", execute: async () => sanitizeAiResult(await dependencies.invoke("plugin_host_list", {})) },
    {
      module: "plugin", target: "none", tool: tool("plugin.set_enabled", "Enable or disable one built-in plugin.", { id: { type: "string" }, enabled: { type: "boolean" } }, ["id", "enabled"]), mode: "write", risk: "write",
      approvalPreview: ({ input }) => `${input.enabled === true ? "Enable" : "Disable"} plugin ${requiredString(input, "id", "plugin.set_enabled")}`,
      execute: ({ input }) => dependencies.invoke("plugin_host_set_enabled", { id: requiredString(input, "id", "plugin.set_enabled"), enabled: input.enabled === true }),
    },
    {
      module: "plugin", target: "none", tool: tool("plugin.invoke", "Invoke a plugin action with explicit user-approved payload.", { id: { type: "string" }, action: { type: "string" }, payload: { type: "string" } }, ["id", "action"]), mode: "write", risk: "dangerous",
      approvalPreview: ({ input }) => `Invoke plugin ${requiredString(input, "id", "plugin.invoke")} action ${requiredString(input, "action", "plugin.invoke")}`,
      // The action's own declared risk decides approval; unknown actions stay dangerous
      // and are refused by the host when prepared.
      resolveRisk: async ({ input }) => {
        const id = requiredString(input, "id", "plugin.invoke");
        const action = requiredString(input, "action", "plugin.invoke");
        const described = await dependencies.invoke<{ actions?: { id?: string; aiCallable?: boolean; risk?: string }[] }>("plugin_host_describe", { id })
          .catch(() => undefined);
        const declared = described?.actions?.find((item) => item.id === action && item.aiCallable === true);
        return PLUGIN_ACTION_RISKS.has(declared?.risk ?? "") ? declared!.risk as AiCapabilityRisk : "dangerous";
      },
      execute: async ({ input, signal }) => {
        if (signal.aborted) throw new Error("插件操作已取消");
        const id = requiredString(input, "id", "plugin.invoke");
        const action = requiredString(input, "action", "plugin.invoke");
        const payload = typeof input.payload === "string" ? input.payload : "{}";
        const prepared = await dependencies.invoke<{ ticket: string; risk: string; description: string; permissions: string[] }>("plugin_host_ai_prepare", { id, action, payload });
        // The capability registry has already enforced the conversation's AI approval
        // policy. Keep the ticket private and bind execution to the validated payload;
        // do not introduce a second, modal approval flow here.
        if (signal.aborted) {
          await dependencies.invoke("plugin_host_ai_execute", { ticket: prepared.ticket, approved: false }).catch(() => undefined);
          throw new Error("插件操作已取消");
        }
        return sanitizeAiResult(await dependencies.invoke("plugin_host_ai_execute", { ticket: prepared.ticket, approved: true }));
      },
    },
  ];
}
