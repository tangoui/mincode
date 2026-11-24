import type { WorkspaceTab, WorkspaceView } from "@/types/workspace";
import type { AiAssistantSnapshot } from "@/stores/ai-assistant";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

export const POPUP_TAB_WINDOW_CLOSING_EVENT = "popup-tab-window-closing";
export const POPUP_TAB_STATE_EVENT = "popup-tab-state";
export const AI_ASSISTANT_POPUP_LABEL = "popup-ai-assistant";

export interface PopupTerminalLayoutState {
  type: "terminal-layout";
  windowLabel: string;
  snapshot: unknown;
}

export interface PopupTabData {
  popupType: "tab";
  tab: WorkspaceTab;
}

export interface AiAssistantPopupData {
  popupType: "ai-assistant";
  snapshot: AiAssistantSnapshot;
  timelineExpansion?: Array<[string, boolean]>;
}

export interface PopupAiAssistantState {
  type: "ai-assistant";
  windowLabel: string;
  snapshot: AiAssistantSnapshot;
}

/** A popped-out terminal closed its last terminal tab: the owning window closes the workbench tab. */
export interface PopupCloseWorkbenchTabState {
  type: "close-workbench-tab";
  windowLabel: string;
}

export type PopupWindowState = PopupTerminalLayoutState | PopupAiAssistantState | PopupCloseWorkbenchTabState;
export type PopupWindowData = PopupTabData | AiAssistantPopupData;

// Notes keep their open drafts in one window's store; a second window would save over the other's edits.
const nonPopoutViews = new Set<WorkspaceView>(["dashboard", "settings", "ai", "notes"]);
let popupDataPromise: Promise<PopupWindowData | null> | undefined;

export function isPopupWindow() {
  return new URLSearchParams(window.location.search).get("mode") === "popup-tab";
}

export function popupWindowLabel() {
  return new URLSearchParams(window.location.search).get("label")?.trim() || "";
}

export function canPopOutTab(tab: Pick<WorkspaceTab, "view">) {
  return !nonPopoutViews.has(tab.view);
}

function popupLabelForTab(tab: WorkspaceTab) {
  const suffix = typeof crypto.randomUUID === "function"
    ? crypto.randomUUID()
    : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return `popup-${tab.id.replace(/[^A-Za-z0-9_-]/g, "-")}-${suffix}`;
}

export async function popOutTab(tab: WorkspaceTab, title: string) {
  if (!isDesktopRuntime()) throw new Error("desktop-required");
  if (!canPopOutTab(tab)) throw new Error("unsupported-tab");

  const label = popupLabelForTab(tab);
  const payload: PopupTabData = { popupType: "tab", tab: { ...tab } };
  await invokeBackend<void>("create_popup_tab_window", {
    label,
    tabData: JSON.stringify(payload),
    title,
  });
  return label;
}

export async function popOutAiAssistant(title: string, snapshot: AiAssistantSnapshot, timelineExpansion?: Array<[string, boolean]>) {
  if (!isDesktopRuntime()) throw new Error("desktop-required");
  const payload: AiAssistantPopupData = { popupType: "ai-assistant", snapshot, timelineExpansion };
  await invokeBackend<void>("create_popup_tab_window", {
    label: AI_ASSISTANT_POPUP_LABEL,
    tabData: JSON.stringify(payload),
    title,
  });
  return AI_ASSISTANT_POPUP_LABEL;
}

/** The native store consumes the payload once; cache it for Vue dev double-mounts. */
export function getPopupWindowData(): Promise<PopupWindowData | null> {
  if (!popupDataPromise) {
    popupDataPromise = (async () => {
      const label = popupWindowLabel();
      for (let attempt = 0; attempt < 5; attempt += 1) {
        const raw = await invokeBackend<string | null>("get_popup_tab_data", { label });
        if (raw) return JSON.parse(raw) as PopupWindowData;
        await new Promise((resolve) => window.setTimeout(resolve, 160));
      }
      return null;
    })();
  }
  return popupDataPromise;
}

export function isPopupTabData(data: PopupWindowData): data is PopupTabData {
  return data.popupType === "tab";
}

export async function focusPopupWindow(label: string) {
  await invokeBackend<void>("focus_popup_tab_window", { label });
}

export async function closePopupWindow(label: string) {
  await invokeBackend<void>("close_popup_tab_window", { label });
}

export interface PopupWorkbenchTabCloser {
  readonly tabs: readonly WorkspaceTab[];
  readonly poppedOutTabLabels: Readonly<Record<string, string>>;
  readonly activeTabId: string;
  closeTab(id: string, force: boolean): void;
  /** Remember the tab for "reopen closed tab" (recordClosedTabs). */
  recordClosed(tabs: WorkspaceTab[]): void;
  /** Called when the closed tab was the active one. */
  navigateToActive(): void;
  closeWindow(label: string): Promise<void>;
}

/**
 * Main window: closes the workbench tab popped out to `windowLabel` the way the tab
 * bar's × does (record, close, close the popup window), so the terminal cleanup ends
 * its shells. Only the window that owns the popup acts, and never on a pinned tab (the
 * tab bar gives those no ×); returns the closed tab id.
 */
export function closePoppedOutWorkbenchTab(windowLabel: string, closer: PopupWorkbenchTabCloser) {
  const tabId = Object.entries(closer.poppedOutTabLabels).find(([, label]) => label === windowLabel)?.[0];
  const tab = tabId ? closer.tabs.find((candidate) => candidate.id === tabId) : undefined;
  if (!tab || tab.pinned) return undefined;
  const wasActive = closer.activeTabId === tab.id;
  closer.recordClosed([tab]);
  // Close before the window goes: its closing event would otherwise reclaim the tab.
  closer.closeTab(tab.id, true);
  if (wasActive) closer.navigateToActive();
  void closer.closeWindow(windowLabel).catch(() => undefined);
  return tab.id;
}
