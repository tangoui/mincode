// views[].prefetch: the host invokes one read-only UI action through the new
// session while the frame loads, then pushes the outcome to that session's
// page once after the handshake (`transport.prefetched(action)`).
import { PLUGIN_UI_MAX_MESSAGE_BYTES, pluginUIMessageByteLength } from "./protocol.ts";

export type PluginUIPrefetchOutcome =
  | { ok: true; result: unknown }
  | { ok: false; error: { code: string; message: string } };

export interface PluginUIPrefetchMessage {
  type: "mincode.host.prefetch";
  action: string;
  ok: boolean;
  result?: unknown;
  error?: { code: string; message: string };
}

/**
 * The bounded push for one prefetch outcome. A result above the ordinary
 * response limit becomes a `result-too-large` failure, so the page falls back
 * to calling the action itself.
 */
export function pluginUIPrefetchMessage(action: string, outcome: PluginUIPrefetchOutcome, tooLargeMessage: string): PluginUIPrefetchMessage {
  const message: PluginUIPrefetchMessage = outcome.ok
    ? { type: "mincode.host.prefetch", action, ok: true, result: outcome.result }
    : { type: "mincode.host.prefetch", action, ok: false, error: outcome.error };
  if (pluginUIMessageByteLength(message, PLUGIN_UI_MAX_MESSAGE_BYTES) <= PLUGIN_UI_MAX_MESSAGE_BYTES) return message;
  return { type: "mincode.host.prefetch", action, ok: false, error: { code: "result-too-large", message: tooLargeMessage } };
}
