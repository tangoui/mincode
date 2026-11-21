/**
 * Holds on a project's language server: while any is held the LSP runtime
 * never idle-stops that session (lspIdleStopSeconds), e.g. jdtls while it
 * hosts a Java debug session or a Java launch is loading into it. Also the
 * launches in flight that restart a built-in server themselves (no "restart
 * required" prompt meanwhile). Transient, never persisted; dependency-free so
 * callers stay out of the LSP runtime chunk.
 */
/**
 * Why a server is held: "launch" while a Java launch loads into it,
 * "debug-session" while it hosts a running debug session (restarting it
 * would end that session).
 */
export type LspServerHoldReason = "launch" | "debug-session";

const holds = new Map<string, Map<LspServerHoldReason, number>>();
const releasedListeners = new Set<(root: string, serverId: string) => void>();

/** Project roots compare without trailing separators. */
export function lspRootKey(root: string): string {
  return root.length > 1 ? root.replace(/[\\/]+$/, "") : root;
}

function holdKey(root: string, serverId: string) {
  return `${lspRootKey(root)}\0${serverId}`;
}

/** Holds serverId of root; the returned release is idempotent. */
export function holdLspServer(root: string, serverId: string, reason: LspServerHoldReason = "debug-session"): () => void {
  const key = holdKey(root, serverId);
  const counts = holds.get(key) ?? new Map<LspServerHoldReason, number>();
  holds.set(key, counts);
  counts.set(reason, (counts.get(reason) ?? 0) + 1);
  let released = false;
  return () => {
    if (released) return;
    released = true;
    const count = (counts.get(reason) ?? 1) - 1;
    if (count > 0) counts.set(reason, count);
    else counts.delete(reason);
    if (counts.size > 0) return;
    holds.delete(key);
    for (const listener of [...releasedListeners]) listener(root, serverId);
  };
}

/** Held for reason, or for any reason when reason is omitted. */
export function isLspServerHeld(root: string, serverId: string, reason?: LspServerHoldReason): boolean {
  const counts = holds.get(holdKey(root, serverId));
  if (!counts) return false;
  return reason === undefined ? counts.size > 0 : (counts.get(reason) ?? 0) > 0;
}

/** Called when the last hold (of any reason) of a server is released. */
export function onLspServerReleased(listener: (root: string, serverId: string) => void): () => void {
  releasedListeners.add(listener);
  return () => { releasedListeners.delete(listener); };
}

const launches = new Map<string, number>();

/**
 * Marks a launch (Java debugging) that restarts serverId itself as in
 * flight; returns the idempotent end.
 */
export function beginBuiltinServerLaunch(serverId: string): () => void {
  launches.set(serverId, (launches.get(serverId) ?? 0) + 1);
  let ended = false;
  return () => {
    if (ended) return;
    ended = true;
    const count = (launches.get(serverId) ?? 1) - 1;
    if (count > 0) launches.set(serverId, count);
    else launches.delete(serverId);
  };
}

export function builtinServerLaunchInFlight(serverId: string): boolean {
  return (launches.get(serverId) ?? 0) > 0;
}
