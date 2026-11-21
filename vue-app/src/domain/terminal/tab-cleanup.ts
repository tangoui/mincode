/**
 * Pane IDs that some open terminal tab showed before and none shows now: their
 * tab was closed (or, with shared workspace layouts, their slot went away). Their
 * shells can be closed and cached screens dropped. Panes moved between tabs or
 * hidden behind another tab are still shown by an open tab and are kept.
 */
export function releasedTerminalPanes(before: Iterable<string>, after: Iterable<string>): string[] {
  const kept = new Set(after);
  return [...new Set(before)].filter((paneId) => !kept.has(paneId));
}
