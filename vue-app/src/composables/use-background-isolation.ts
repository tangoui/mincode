/**
 * Makes every sibling of a full-screen modal surface inert, so keyboard focus
 * and pointer input cannot reach controls hidden underneath it. An overlay on
 * its own only covers the page visually: Tab would still move focus into the
 * app (for example a terminal) and keystrokes would act there.
 *
 * Siblings appended while isolated (teleported dialogs, drawers, toasts) are
 * made inert as well. Elements that were already inert keep that state on
 * release.
 */
export function isolateBackground(keep: Element, container: Element = document.body): () => void {
  const isolated = new Set<Element>();
  const isolate = (element: Element) => {
    if (element === keep || element.contains(keep) || element.hasAttribute("inert")) return;
    element.setAttribute("inert", "");
    isolated.add(element);
  };
  for (const child of Array.from(container.children)) isolate(child);
  const observer = new MutationObserver((records) => {
    for (const record of records) {
      for (const node of Array.from(record.addedNodes)) {
        if (node instanceof Element) isolate(node);
      }
    }
  });
  observer.observe(container, { childList: true });
  return () => {
    observer.disconnect();
    for (const element of isolated) element.removeAttribute("inert");
    isolated.clear();
  };
}
