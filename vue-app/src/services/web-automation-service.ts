import { invokeBackend } from "@/services/desktop-bridge";

export type SystemBrowser = "default" | "chrome" | "safari" | "firefox" | "edge";

export interface WebAutomationDriverStatus {
  id: string;
  name: string;
  available: boolean;
  platform: string;
  capabilities: string[];
  message?: string;
}

export interface OpenSystemBrowserResult {
  url: string;
  browser: SystemBrowser;
  platform: string;
  captureError?: string;
}

/**
 * Opens a normal webpage outside MinCode without starting a Computer Use
 * screenshot cycle. AI automation continues through the registered driver.
 */
export function openSystemBrowser(url: string, browser: SystemBrowser = "default") {
  return invokeBackend<OpenSystemBrowserResult>("system_browser_open_url", { url, browser });
}

export function listWebAutomationDrivers() {
  return invokeBackend<WebAutomationDriverStatus[]>("web_automation_list_drivers", {});
}
