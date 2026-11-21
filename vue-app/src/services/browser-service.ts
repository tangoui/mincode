import { invokeBackend } from "@/services/desktop-bridge";

// 入口包（App.vue、锁屏）需要的浏览器调用；只给浏览器工作区用的放在 browser-page-service.ts。

export function hideEmbeddedBrowser() {
  return invokeBackend<void>("embedded_browser_hide", {});
}

export function completeEmbeddedBrowserOpenRequest(input: {
  requestId: string;
  tabId?: string;
  url?: string;
  error?: string;
}) {
  return invokeBackend<void>("embedded_browser_complete_open_request", { payload: input });
}

/** Answers a backend `browser:ai-tab-request` (ACP MCP browser.list_tabs / browser.switch_tab). */
export function completeEmbeddedBrowserTabRequest(input: {
  requestId: string;
  result?: unknown;
  error?: string;
}) {
  return invokeBackend<void>("embedded_browser_complete_tab_request", { payload: input });
}
