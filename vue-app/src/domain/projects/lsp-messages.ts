// What a language server's window/showMessage and failed commands show the user.
import { parsePluginLspServerId } from "./lsp.ts";

/** Info messages count as command feedback for this long after the user ran a server command. */
export const LSP_COMMAND_FEEDBACK_MS = 30_000;
export const LSP_MESSAGE_MAX_CHARS = 300;

export type LspToastKind = "error" | "warning" | "info";

export interface LspToast {
  kind: LspToastKind;
  message: string;
}

function oneLine(text: string): string {
  const flat = text.replace(/\s+/g, " ").trim();
  return flat.length > LSP_MESSAGE_MAX_CHARS ? `${flat.slice(0, LSP_MESSAGE_MAX_CHARS - 1)}…` : flat;
}

/**
 * window/showMessage → toast. Errors and warnings always show; info only as
 * feedback shortly after a user-run command (servers also announce routine
 * startup steps as info); log-level messages never.
 */
export function lspShowMessageToast(params: unknown, msSinceUserCommand: number | undefined): LspToast | null {
  const { type, message } = (params && typeof params === "object" ? params : {}) as { type?: unknown; message?: unknown };
  if (typeof message !== "string") return null;
  const text = oneLine(message);
  if (!text) return null;
  if (type === 1) return { kind: "error", message: text };
  if (type === 2) return { kind: "warning", message: text };
  if (type === 3 && msSinceUserCommand !== undefined && msSinceUserCommand <= LSP_COMMAND_FEEDBACK_MS) {
    return { kind: "info", message: text };
  }
  return null;
}

/** The reason a workspace/executeCommand failed, as one bounded line. */
export function lspCommandFailureText(error: unknown): string {
  const raw = error instanceof Error ? error.message : typeof error === "string" ? error : "";
  return oneLine(raw);
}

/** `gopls` stays `gopls`; `plugin:<pluginId>/<serverId>` shows as `<serverId>`. */
export function lspServerShortName(serverId: string): string {
  return parsePluginLspServerId(serverId)?.serverId ?? serverId;
}
