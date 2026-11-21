// Shows language-server window/showMessage and failed server commands as toasts.
import { ElMessage } from "element-plus";
import { useVueI18n } from "@/i18n/messages";
import { lspServerShortName, type LspToast } from "@/domain/projects/lsp-messages";

const REPEAT_WINDOW_MS = 5_000;
const recent = new Map<string, number>();

function firstTimeRecently(key: string): boolean {
  const now = Date.now();
  for (const [seen, at] of recent) if (now - at > REPEAT_WINDOW_MS) recent.delete(seen);
  if (recent.has(key)) return false;
  recent.set(key, now);
  return true;
}

/** A server's message; the same text from the same server shows once per few seconds. */
export function showLspMessage(serverId: string, toast: LspToast) {
  if (!firstTimeRecently(`${serverId}\0${toast.kind}\0${toast.message}`)) return;
  const server = lspServerShortName(serverId);
  ElMessage({ type: toast.kind, message: useVueI18n().t("project.lsp.serverMessage", { server, message: toast.message }), grouping: true });
}

/** A workspace/executeCommand the user ran (CodeLens, code action) failed. */
export function showLspCommandFailure(serverId: string, title: string, reason: string) {
  const { t } = useVueI18n();
  const server = lspServerShortName(serverId);
  const name = title || t("project.lsp.commandUntitled");
  ElMessage.error(reason
    ? t("project.lsp.commandFailed", { server, title: name, reason })
    : t("project.lsp.commandFailedNoReason", { server, title: name }));
}
