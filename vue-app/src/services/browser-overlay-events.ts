export const EMBEDDED_BROWSER_OVERLAY_BLOCK_EVENT = "mincode:embedded-browser-overlay-block";

interface EmbeddedBrowserOverlayBlockDetail {
  source: string;
  blocked: boolean;
}

const blockers = new Set<string>();

export function setEmbeddedBrowserOverlayBlocked(source: string, blocked: boolean) {
  if (blocked) blockers.add(source);
  else blockers.delete(source);

  window.dispatchEvent(
    new CustomEvent<EmbeddedBrowserOverlayBlockDetail>(EMBEDDED_BROWSER_OVERLAY_BLOCK_EVENT, {
      detail: { source, blocked },
    })
  );
}

export function isEmbeddedBrowserOverlayBlocked() {
  return blockers.size > 0;
}
