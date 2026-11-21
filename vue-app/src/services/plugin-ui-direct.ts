import { parsePluginUIDirectToken } from "@/domain/plugin-ui/direct-token";
import { invokeBackend } from "@/services/desktop-bridge";

export async function requestPluginUIDirectToken(sessionId: string, nonce: string) {
  return parsePluginUIDirectToken(await invokeBackend<unknown>("plugin_host_ui_direct_token", { sessionId, nonce }));
}
