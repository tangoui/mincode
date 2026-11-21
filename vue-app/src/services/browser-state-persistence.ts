import { createWorkspaceScopedPersistence } from "@/services/workspace-scoped-persistence";

export const BROWSER_STATE_STORAGE_KEY = "app_browser_state_v1";

export interface PersistedBrowserTab {
  id: string;
  url: string;
  title: string;
}

export interface PersistedBrowserState {
  version: 1;
  tabs: PersistedBrowserTab[];
  activeTabId: string;
}

/** Browser preview keeps tab order but never stores URLs or page titles that may contain secrets. */
export function sanitizeBrowserStateForPreview(
  value: PersistedBrowserState
): PersistedBrowserState {
  return {
    ...value,
    tabs: value.tabs.map((tab) => ({ ...tab, url: "", title: "" })),
  };
}

const browserStatePersistence = createWorkspaceScopedPersistence<PersistedBrowserState>(
  BROWSER_STATE_STORAGE_KEY,
  {
    secure: true,
    browserSnapshot: sanitizeBrowserStateForPreview,
  }
);

export const loadBrowserState = browserStatePersistence.load;
export const saveBrowserState = browserStatePersistence.save;
