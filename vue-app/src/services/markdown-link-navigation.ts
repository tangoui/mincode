import { isDesktopRuntime, openExternalUrl } from "@/services/desktop-bridge";

export async function openWorkspaceLink(url: string): Promise<void> {
  if (isDesktopRuntime()) {
    await openExternalUrl(url);
  } else {
    window.open(url, "_blank", "noopener,noreferrer");
  }
}
