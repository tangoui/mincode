/**
 * Keyboard access for rows that open an AppContextMenu: the ContextMenu key
 * and Shift+F10 open the same menu as a right-click, anchored below the row.
 */
export function isContextMenuKey(event: KeyboardEvent): boolean {
  return event.key === "ContextMenu" || (event.shiftKey && event.key === "F10");
}

let disarmContextMenuGuard: (() => void) | undefined;

/**
 * Chromium and WebView2 follow a Shift+F10 / ContextMenu keydown with a native
 * `contextmenu` event (at the pointer), whatever the keydown handler did. This
 * swallows that one event so a menu opened from the keyboard is not reopened
 * at the mouse position. A real right-click (its mousedown) or a timeout
 * disarms the guard first.
 */
function suppressFollowingContextMenu(timeoutMs = 1000) {
  disarmContextMenuGuard?.();
  const swallow = (event: Event) => {
    event.preventDefault();
    event.stopPropagation();
    disarm();
  };
  const disarm = () => {
    window.removeEventListener("contextmenu", swallow, true);
    window.removeEventListener("mousedown", disarm, true);
    clearTimeout(timer);
    if (disarmContextMenuGuard === disarm) disarmContextMenuGuard = undefined;
  };
  const timer = setTimeout(disarm, timeoutMs);
  window.addEventListener("contextmenu", swallow, true);
  window.addEventListener("mousedown", disarm, true);
  disarmContextMenuGuard = disarm;
}

/**
 * For a row's keydown: true when it is the ContextMenu key or Shift+F10. The
 * key's default and the native `contextmenu` that follows it are suppressed,
 * so the caller opens its menu exactly once.
 */
export function claimContextMenuKey(event: KeyboardEvent): boolean {
  if (!isContextMenuKey(event)) return false;
  event.preventDefault();
  suppressFollowingContextMenu();
  return true;
}

/**
 * Where a row's context menu opens: the pointer, or below the focused row for
 * the keyboard (`row`, else the element the handler is on).
 */
export function contextMenuAnchor(event: MouseEvent | KeyboardEvent, row?: Element): { x: number; y: number } {
  if ("clientX" in event) return { x: event.clientX, y: event.clientY };
  const rect = (row ?? (event.currentTarget as Element | null))?.getBoundingClientRect();
  return rect ? { x: rect.left + 16, y: rect.bottom } : { x: 0, y: 0 };
}
