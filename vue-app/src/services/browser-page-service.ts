import { invokeBackend } from "@/services/desktop-bridge";

// 只给浏览器工作区（懒加载）用的后端调用和错误解析；放在 browser-service.ts
// 之外，免得它们跟着 App.vue 进入入口包。

export interface BrowserToolError {
  code?: string;
  message: string;
  details?: Record<string, unknown>;
}

/**
 * 浏览器工具错误可能是后端的结构化 JSON 文本 `{"code","message","details"}`，
 * 也可能是普通文本；给用户显示时取 message，给 AI 时保留原文（含 code）。
 */
export function parseBrowserToolError(error: unknown): BrowserToolError {
  const text = error instanceof Error ? error.message : String(error ?? "");
  const trimmed = text.trim();
  if (trimmed.startsWith("{")) {
    try {
      const parsed = JSON.parse(trimmed) as Record<string, unknown> | null;
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)
        && typeof parsed.code === "string" && typeof parsed.message === "string") {
        const details = parsed.details;
        return {
          code: parsed.code,
          message: parsed.message,
          ...(details && typeof details === "object" && !Array.isArray(details)
            ? { details: details as Record<string, unknown> }
            : {}),
        };
      }
    } catch {
      // Not structured; fall through to the plain message.
    }
  }
  return { message: text };
}

/** The page message bar's answer to a page dialog (recorded by the backend as by=user). */
export function completeEmbeddedBrowserDialog(input: {
  tabId: string;
  dialogId: string;
  accept: boolean;
  text: string;
}) {
  return invokeBackend<void>("embedded_browser_complete_dialog", { payload: input });
}

/** Shows a completed download in Finder; the saved path never leaves the backend. */
export function revealEmbeddedBrowserDownload(id: string) {
  return invokeBackend<void>("embedded_browser_reveal_download", { payload: { id } });
}

export interface EmbeddedBrowserBounds {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface EmbeddedBrowserReadyState {
  readyState: "loading" | "interactive" | "complete";
  url: string;
  title: string;
}

export interface EmbeddedBrowserAiAccess {
  enabled: boolean;
  resourceId: string;
  sharedTabIds: string[];
  targetTabId: string;
  tabId: string;
}

export function syncEmbeddedBrowserBounds(tabId: string, bounds: EmbeddedBrowserBounds) {
  return invokeBackend<void>("embedded_browser_sync_bounds", {
    payload: { tabId, ...bounds, visible: true },
  });
}

export function mountEmbeddedBrowser(tabId: string, url: string, bounds: EmbeddedBrowserBounds) {
  return invokeBackend<void>("embedded_browser_mount", {
    payload: { tabId, url, ...bounds },
  });
}

export function activateEmbeddedBrowser(tabId: string) {
  return invokeBackend<void>("embedded_browser_activate", { payload: { tabId } });
}

export function closeEmbeddedBrowserTab(tabId: string) {
  return invokeBackend<void>("embedded_browser_close_tab", { payload: { tabId } });
}

export function navigateEmbeddedBrowser(tabId: string, url: string) {
  return invokeBackend<void>("embedded_browser_navigate", { payload: { tabId, url } });
}

export function getEmbeddedBrowserUrl(tabId: string) {
  return invokeBackend<string>("embedded_browser_get_url", { payload: { tabId } });
}

export function waitEmbeddedBrowserReady(
  tabId: string,
  timeoutMs = 15_000,
  expectedUrl = "",
  previousUrl = ""
) {
  return invokeBackend<EmbeddedBrowserReadyState>("embedded_browser_wait_ready", {
    payload: { tabId, timeoutMs, expectedUrl, previousUrl },
  });
}

export function embeddedBrowserExists() {
  return invokeBackend<boolean>("embedded_browser_exists", {});
}

export function evaluateEmbeddedBrowser<T = unknown>(tabId: string, script: string, timeoutMs = 3_000) {
  return invokeBackend<T>("embedded_browser_eval", { payload: { tabId, script, timeoutMs } });
}


/** 把共享给 AI 的标签页集合和 AI 目标页同步到后端；目标不在集合里时后端会清空它。 */
export async function setEmbeddedBrowserAiAccess(sharedTabIds: string[], targetTabId: string): Promise<void> {
  await invokeBackend<EmbeddedBrowserAiAccess>("embedded_browser_set_ai_access", {
    payload: { sharedTabIds, targetTabId },
  });
}



export function runEmbeddedBrowserCommand(
  command:
    | "embedded_browser_reload"
    | "embedded_browser_toggle_devtools"
    | "embedded_browser_clear_browsing_data",
  tabId?: string
) {
  return invokeBackend<void>(command, tabId ? { payload: { tabId } } : {});
}
