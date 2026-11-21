/**
 * Plugin service results opened in a read-only workbench tab (design §2b,
 * §11): "Open in new tab" in PluginServiceResultDialog.vue stores the result
 * here and opens a `service-result` tab, rendered by
 * views/PluginServiceResultWorkspaceView.vue with a read-only Monaco viewer.
 *
 * Memory only: a bounded map keyed by a random result id. A document lives
 * while a workbench tab shows it and is dropped when that tab closes (or the
 * workspace holding it is deleted); the tab itself is never written to the
 * session store (domain/workspace/workbench-session.ts `isTransientTab`). No
 * file is written and nothing enters recent files.
 */
import { shallowReactive, watch } from "vue";
import type { Router } from "vue-router";
import { workspaceTabRoute } from "@/domain/workspace/tab-route";
import type { WorkspaceTab } from "@/types/workspace";

export interface PluginServiceResultDocument {
  id: string;
  /** Service display title, without the time the tab title adds. */
  title: string;
  kind: "text" | "json";
  language: "json" | "plaintext";
  /** The result text; JSON already pretty-printed. */
  text: string;
  createdAt: number;
}

/** Oldest documents beyond this are dropped; their tabs then say the result is gone. */
export const MAX_PLUGIN_SERVICE_RESULT_DOCUMENTS = 32;

const documents = shallowReactive(new Map<string, PluginServiceResultDocument>());

interface ServiceResultTabStore {
  readonly tabs: readonly WorkspaceTab[];
  openServiceResult(resultId: string, title: string): WorkspaceTab | undefined;
}

function createResultId() {
  return globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

export function pluginServiceResultDocument(id: string): PluginServiceResultDocument | undefined {
  return documents.get(id);
}

/**
 * Stores `result` and opens it in a new read-only tab of the active workspace,
 * routed to right away. Returns undefined (and keeps nothing) when the tab
 * quota refuses the tab.
 */
export function openPluginServiceResultTab(
  workbench: ServiceResultTabStore,
  router: Pick<Router, "push">,
  result: { title: string; kind: "text" | "json"; text: string },
  tabTitle: string,
): WorkspaceTab | undefined {
  const id = createResultId();
  documents.set(id, {
    id,
    title: result.title,
    kind: result.kind,
    language: result.kind === "json" ? "json" : "plaintext",
    text: result.text,
    createdAt: Date.now(),
  });
  for (const key of documents.keys()) {
    if (documents.size <= MAX_PLUGIN_SERVICE_RESULT_DOCUMENTS) break;
    documents.delete(key);
  }
  const tab = workbench.openServiceResult(id, tabTitle);
  if (!tab) {
    documents.delete(id);
    return undefined;
  }
  void router.push(workspaceTabRoute(tab));
  return tab;
}

/**
 * Drops every document no workbench tab shows any more, now and whenever the
 * tabs change. Installed by PluginServicesRoot.vue in the main window.
 */
export function installPluginServiceResultTabCleanup(workbench: Pick<ServiceResultTabStore, "tabs">): () => void {
  return watch(
    () => workbench.tabs.map((tab) => tab.serviceResult?.resultId).filter((id): id is string => Boolean(id)),
    (ids) => {
      const open = new Set(ids);
      for (const id of [...documents.keys()]) if (!open.has(id)) documents.delete(id);
    },
    { immediate: true },
  );
}
