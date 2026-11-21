import type { MessageKey } from "@/i18n/locales/zh";

const HOST_SHORTCUT_OCCUPIED = /(?:^|\s)global-shortcut-occupied:\s*(\S.*?)\s*$/;

interface HostGlobalShortcutErrorNotice {
  key: MessageKey;
  params: Record<string, string>;
}

export function hostGlobalShortcutErrorNotice(message: string): HostGlobalShortcutErrorNotice {
  const occupied = HOST_SHORTCUT_OCCUPIED.exec(message);
  if (occupied) return { key: "settings.globalShortcutOccupied", params: { key: occupied[1] } };
  return { key: "settings.globalShortcutFailed", params: { error: message } };
}
