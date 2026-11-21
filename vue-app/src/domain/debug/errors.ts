/**
 * What the debug UI shows for a failed host or DAP request. The host rejects
 * an adapter's failure response as "dap <command> failed: <adapter text>"
 * (backend/debug/session.go RequestError), possibly inside its own
 * context ("internal: debug begin: dap launch failed: …"), and its own failures with
 * a code prefix (commands.go). The user sees the adapter's own text, and
 * localized copy for a request that timed out or was cancelled; any other
 * message is shown as it is. Code that branches on a code (e.g. restart on
 * `not_found:`) reads the raw message, not this text.
 */
import { useVueI18n } from "@/i18n/messages";

/** The host's request prefix, at the start or after the host's own context (e.g. "debug begin: dap launch failed: "); the first one only. */
const HOST_REQUEST_PREFIX = /^(?:.*?: )??dap \S+ failed: /s;

export function debugErrorText(error: unknown): string {
  const message = error instanceof Error ? error.message : String(error);
  const { t } = useVueI18n();
  if (message.startsWith("timeout:")) return t("debug.error.timeout");
  if (message.startsWith("cancelled:")) return t("debug.error.cancelled");
  return message.replace(HOST_REQUEST_PREFIX, "");
}
