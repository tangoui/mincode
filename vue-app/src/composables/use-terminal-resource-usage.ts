import { onActivated, onBeforeUnmount, onDeactivated, onMounted, shallowRef } from "vue";
import type { LocalShellUsage } from "@/domain/terminal/resource-usage";
import { fetchLocalShellUsage } from "@/services/terminal-resource-usage";

export const TERMINAL_USAGE_POLL_MS = 5000;

/**
 * Memory of the local terminal sessions sessionIds() returns, by session id, refreshed
 * every few seconds while the owning view is shown (not while KeepAlive keeps it in the
 * background or the window is hidden). Each refresh lists every process once on the backend.
 */
export function useTerminalResourceUsage(sessionIds: () => string[]) {
  const usages = shallowRef<ReadonlyMap<string, LocalShellUsage>>(new Map());
  let timer: ReturnType<typeof setInterval> | undefined;
  let pending = false;
  let running = false;

  async function refresh() {
    if (!running || pending || document.visibilityState === "hidden") return;
    const ids = sessionIds();
    if (ids.length === 0) {
      usages.value = new Map();
      return;
    }
    pending = true;
    try {
      const list = await fetchLocalShellUsage(ids);
      if (running) usages.value = new Map(list.map((usage) => [usage.sessionId, usage]));
    } catch {
      // Best effort: keep the last values; the next refresh tries again.
    } finally {
      pending = false;
    }
  }

  function start() {
    stop();
    running = true;
    void refresh();
    timer = setInterval(() => void refresh(), TERMINAL_USAGE_POLL_MS);
  }

  function stop() {
    running = false;
    if (timer) clearInterval(timer);
    timer = undefined;
  }

  function handleVisibility() {
    if (document.visibilityState === "visible") void refresh();
  }

  onMounted(() => {
    document.addEventListener("visibilitychange", handleVisibility);
    start();
  });
  onActivated(start);
  onDeactivated(stop);
  onBeforeUnmount(() => {
    document.removeEventListener("visibilitychange", handleVisibility);
    stop();
  });

  return { usages, refresh };
}
