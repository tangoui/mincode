import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it, vi } from "vitest";
import { closePoppedOutWorkbenchTab, type PopupWorkbenchTabCloser } from "@/services/popup-tab";
import type { WorkspaceTab } from "@/types/workspace";

function tab(id: string): WorkspaceTab {
  return { id, workspaceId: "w", view: "terminal", titleKey: "nav.terminal", closable: true };
}

function closer(overrides: Partial<PopupWorkbenchTabCloser> = {}) {
  const calls: string[] = [];
  const value: PopupWorkbenchTabCloser = {
    tabs: [tab("a"), tab("b")],
    poppedOutTabLabels: { b: "popup-b" },
    activeTabId: "a",
    closeTab: vi.fn((id: string, force: boolean) => calls.push(`close:${id}:${force}`)),
    recordClosed: vi.fn((tabs: WorkspaceTab[]) => calls.push(`record:${tabs.map((item) => item.id).join()}`)),
    navigateToActive: vi.fn(() => calls.push("navigate")),
    closeWindow: vi.fn(async (label: string) => {
      calls.push(`window:${label}`);
    }),
    ...overrides,
  };
  return { value, calls };
}

describe("closePoppedOutWorkbenchTab", () => {
  it("closes the workbench tab owning the popup like the tab bar ×, then its window", () => {
    const { value, calls } = closer();
    expect(closePoppedOutWorkbenchTab("popup-b", value)).toBe("b");
    expect(calls).toEqual(["record:b", "close:b:true", "window:popup-b"]);
  });

  it("navigates when the closed tab was active", () => {
    const { value, calls } = closer({ activeTabId: "b" });
    closePoppedOutWorkbenchTab("popup-b", value);
    expect(calls).toEqual(["record:b", "close:b:true", "navigate", "window:popup-b"]);
  });

  it("never closes a pinned workbench tab", () => {
    const { value, calls } = closer({ tabs: [tab("a"), { ...tab("b"), pinned: true }] });
    expect(closePoppedOutWorkbenchTab("popup-b", value)).toBeUndefined();
    expect(calls).toEqual([]);
  });

  it("ignores a label this window does not own", () => {
    const { value, calls } = closer();
    expect(closePoppedOutWorkbenchTab("popup-other", value)).toBeUndefined();
    expect(calls).toEqual([]);
  });

  it("is wired into the main window's popup state listener and sent by the terminal popup", () => {
    const app = readFileSync(resolve(__dirname, "../../src/App.vue"), "utf8");
    expect(app).toMatch(/state\?\.type === "close-workbench-tab"\) \{\s*closePoppedOutWorkbenchTab\(state\.windowLabel,/);
    const view = readFileSync(resolve(__dirname, "../../src/views/TerminalWorkspaceView.vue"), "utf8");
    expect(view).toMatch(/emitAppEvent\(POPUP_TAB_STATE_EVENT, \{ type: "close-workbench-tab", windowLabel: popupLabel \}\)/);
    expect(view).not.toMatch(/closePopupWindow/);
  });

  it("the terminal view refuses to close the last terminal tab of a pinned workbench tab", () => {
    const view = readFileSync(resolve(__dirname, "../../src/views/TerminalWorkspaceView.vue"), "utf8");
    expect(view).toMatch(/workbench\.activeTab\.pinned === true && tabs\.value\.length <= 1/);
    expect(view).toMatch(/if \(lastTabCloseLocked\.value\) \{\s*ElMessage\.warning\(t\("tab\.pinnedCannotClose"\)\);\s*return;/);
    expect(view).toMatch(/if \(tab\?\.view !== "terminal" \|\| tab\.pinned\) return;/);
    expect(view).toMatch(/:close-last-pane="!lastTabCloseLocked"/);
    expect(view.match(/:close-disabled-reason="closeLockedReason"/g)).toHaveLength(2);
  });
});
