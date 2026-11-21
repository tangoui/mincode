/**
 * Localized titles of the host's plugin global shortcut rejection notices.
 * The Go host posts `plugin:notify` with code "globalShortcut.<reason>"
 * (backend/plugin/global_shortcuts.go, globalShortcutRejections) and a
 * bilingual fallback title. Kept separate from plugin-global-shortcuts.ts so
 * App.vue does not pull the contribution contract into the entry chunk.
 */
import type { MessageKey } from "@/i18n/locales/zh";

const NOTICE_REASONS = new Set(["disabled", "inactive", "unavailable", "locked", "startFailed", "busy", "failed"]);

/** The message key for a notice code; undefined for any other code. */
export function globalShortcutNoticeKey(code: unknown): MessageKey | undefined {
  if (typeof code !== "string" || !code.startsWith("globalShortcut.")) return undefined;
  const reason = code.slice("globalShortcut.".length);
  return NOTICE_REASONS.has(reason) ? (`pluginContrib.globalShortcuts.notice.${reason}` as MessageKey) : undefined;
}

/**
 * Error codes of desktop_set_global_shortcut (MinCode's own activation
 * shortcut, app/desktop_runtime.go and backend/desktop/desktop.go):
 * "global-shortcut-occupied: <accelerator>" when a plugin global shortcut
 * holds the combo, "global-shortcut-unavailable" without a native adapter.
 */
const HOST_SHORTCUT_OCCUPIED = /(?:^|\s)global-shortcut-occupied:\s*(\S.*?)\s*$/;

export interface HostGlobalShortcutErrorNotice {
  key: MessageKey;
  params: Record<string, string>;
}

/** The localized notice for a desktop_set_global_shortcut error message. */
export function hostGlobalShortcutErrorNotice(message: string): HostGlobalShortcutErrorNotice {
  const occupied = HOST_SHORTCUT_OCCUPIED.exec(message);
  if (occupied) return { key: "settings.globalShortcutOccupied", params: { key: occupied[1] } };
  return { key: "settings.globalShortcutFailed", params: { error: message } };
}
