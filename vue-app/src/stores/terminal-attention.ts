import { acceptHMRUpdate, defineStore } from "pinia";
import { computed, ref, shallowRef } from "vue";
import {
  attentionFromActivity,
  attentionReasonKey,
  clearAttention,
  parseTerminalAttentionEvent,
  raiseAttention,
  retainAttention,
  TERMINAL_ATTENTION_EVENT,
  type AttentionItems,
  type TerminalAttention,
} from "@/domain/terminal/terminal-attention";
import { useVueI18n } from "@/i18n/messages";
import { notifyTerminalAttention } from "@/services/notifications";
import { readSettingsSnapshot } from "@/services/settings-persistence";
import { isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";

/** Supplied by the workbench: which panes still belong to a tab, and how to name one. */
export interface TerminalAttentionResolver {
  owns(paneId: string): boolean;
  describe(paneId: string): string;
}

// A burst (several panes exiting together) becomes one system notification.
const NOTIFICATION_INTERVAL_MS = 1_500;

export function terminalAttentionEnabled() {
  return readSettingsSnapshot<Record<string, unknown>>().terminalAttention !== false;
}

function notificationsEnabled() {
  return readSettingsSnapshot<Record<string, unknown>>().terminalAttentionSystemNotify !== false;
}

function windowFocused() {
  return typeof document === "undefined" || (document.visibilityState === "visible" && document.hasFocus());
}

/**
 * Terminals that finished, stopped, rang or are waiting for the user. Global and not
 * persisted: PTYs do not survive a restart. Every signal marks its tab and its pane
 * (the pane toolbar chip spells it out for a few seconds), even the one on screen,
 * and becomes a system notification while MinCode is in the background. Typing into
 * the pane, clicking its chip or tab, or switching to it acknowledges it.
 */
export const useTerminalAttentionStore = defineStore("terminal-attention", () => {
  const { t } = useVueI18n();
  const items = shallowRef<AttentionItems>(new Map());
  const appFocused = ref(windowFocused());
  const visibleBySource = shallowRef<ReadonlyMap<string, readonly string[]>>(new Map());
  /** Live panes of the sessions workspace and their tab labels. */
  const sessionPanes = shallowRef<ReadonlyMap<string, string>>(new Map());
  const focusRequest = shallowRef<{ paneId: string; seq: number }>();
  const visiblePanes = computed(() => new Set([...visibleBySource.value.values()].flat()));
  const count = computed(() => items.value.size);
  let resolver: TerminalAttentionResolver = { owns: () => false, describe: () => "" };
  let lastNotificationAt = 0;
  let focusSequence = 0;
  let stopListening: (() => void) | undefined;

  function notifySystem(entry: TerminalAttention) {
    const now = Date.now();
    if (!notificationsEnabled() || now - lastNotificationAt < NOTIFICATION_INTERVAL_MS) return;
    lastNotificationAt = now;
    const title = resolver.describe(entry.paneId) || t("terminal.attention.untitled");
    const reason = t(attentionReasonKey(entry.reason));
    notifyTerminalAttention(title, entry.message ? `${reason} · ${entry.message}` : reason, entry.paneId);
  }

  function report(entry: TerminalAttention | undefined) {
    if (!entry || !terminalAttentionEnabled() || !resolver.owns(entry.paneId)) return;
    const result = raiseAttention(items.value, entry, false);
    if (result.items === items.value) return;
    items.value = result.items;
    if (result.raised && !appFocused.value) notifySystem(entry);
  }

  /**
   * What one surface (terminal workspace, sessions) shows right now. Panes that come
   * on screen while the window is in front count as looked at; the ones already on
   * screen keep their marker until the user types or clicks the tab.
   */
  function setVisiblePanes(source: string, paneIds: readonly string[]) {
    const current = visibleBySource.value.get(source) ?? [];
    if (current.length === paneIds.length && current.every((id, index) => id === paneIds[index])) return;
    const next = new Map(visibleBySource.value);
    if (paneIds.length) next.set(source, [...paneIds]);
    else next.delete(source);
    visibleBySource.value = next;
    if (appFocused.value) {
      const before = new Set(current);
      items.value = clearAttention(items.value, paneIds.filter((paneId) => !before.has(paneId)));
    }
  }

  /** Panes the sessions workspace owns (its live terminals), with their tab labels. */
  function setSessionPanes(panes: Iterable<readonly [string, string]>) {
    sessionPanes.value = new Map(panes);
    prune();
  }

  /** The user typed into a pane or clicked its tab. */
  function dismiss(paneIds: Iterable<string>) {
    items.value = clearAttention(items.value, paneIds);
  }

  function prune() {
    items.value = retainAttention(items.value, (paneId) => resolver.owns(paneId));
  }

  function configure(next: TerminalAttentionResolver) {
    resolver = next;
    prune();
  }

  function requestFocus(paneId: string) {
    focusSequence += 1;
    focusRequest.value = { paneId, seq: focusSequence };
  }

  function syncFocus() {
    appFocused.value = windowFocused();
  }

  function ensureListening() {
    if (stopListening || typeof window === "undefined") return;
    const stops: Array<() => void> = [];
    window.addEventListener("focus", syncFocus);
    window.addEventListener("blur", syncFocus);
    document.addEventListener("visibilitychange", syncFocus);
    stops.push(() => {
      window.removeEventListener("focus", syncFocus);
      window.removeEventListener("blur", syncFocus);
      document.removeEventListener("visibilitychange", syncFocus);
    });
    if (isDesktopRuntime()) {
      stops.push(listenAppEvent<unknown>(TERMINAL_ATTENTION_EVENT, (payload) => report(parseTerminalAttentionEvent(payload))));
      // Claude hook status (terminal_activity_prepare_claude) is the precise signal for Claude panes.
      stops.push(listenAppEvent<unknown>("terminal-activity", (payload) => report(attentionFromActivity(payload))));
    }
    syncFocus();
    stopListening = () => {
      for (const stop of stops) stop();
    };
  }

  function stop() {
    stopListening?.();
    stopListening = undefined;
  }

  return {
    items,
    count,
    appFocused,
    visiblePanes,
    sessionPanes,
    focusRequest,
    report,
    setVisiblePanes,
    setSessionPanes,
    dismiss,
    prune,
    configure,
    requestFocus,
    ensureListening,
    stop,
  };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useTerminalAttentionStore, import.meta.hot));
}
