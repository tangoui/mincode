import { openWorkspaceLink } from "@/services/markdown-link-navigation";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";
import { openSystemBrowser } from "@/services/web-automation-service";

let supported: Promise<boolean> | undefined;

/** Whether this platform has the embedded browser at all (macOS today; not Windows or Linux yet). */
export function embeddedBrowserSupported(): Promise<boolean> {
  supported ??= invokeBackend<unknown>("embedded_browser_supported", {})
    .then((value) => value === true)
    .catch(() => false);
  return supported;
}

/**
 * Opens a link the way the host prefers: the built-in browser where the platform
 * has one, otherwise the system browser. File downloads always go to the system
 * browser, because the built-in browser does not save files.
 */
export async function openHostLink(url: string, options: { download?: boolean } = {}) {
  if (!options.download && isDesktopRuntime() && (await embeddedBrowserSupported())) {
    await openWorkspaceLink(url);
    return;
  }
  await openSystemBrowser(url);
}
