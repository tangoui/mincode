import { WebLinksAddon } from "@xterm/addon-web-links";
import { openSystemBrowser } from "@/services/web-automation-service";
import { isDesktopRuntime } from "@/services/desktop-bridge";

export type TerminalWebLinkFailure = "desktop-required" | "invalid-url" | "open-failed";

interface TerminalWebLinksOptions {
  onFailure: (failure: TerminalWebLinkFailure) => void;
}

/** Accepts only absolute http(s) URLs; returns the normalized href or null. */
export function normalizeWebUrl(value: string): string | null {
  try {
    const url = new URL(value);
    if (url.protocol !== "http:" && url.protocol !== "https:") return null;
    return url.href;
  } catch {
    return null;
  }
}

/**
 * Creates the shared xterm HTTP/HTTPS link handler. Native navigation stays on
 * the existing IPC system-browser boundary instead of opening a webview or
 * using window.open directly.
 */
export function createTerminalWebLinksAddon({ onFailure }: TerminalWebLinksOptions) {
  return new WebLinksAddon((_event, value) => {
    const url = normalizeWebUrl(value);
    if (!url) {
      onFailure("invalid-url");
      return;
    }
    if (!isDesktopRuntime()) {
      onFailure("desktop-required");
      return;
    }

    void openSystemBrowser(url).catch(() => {
      onFailure("open-failed");
    });
  });
}
