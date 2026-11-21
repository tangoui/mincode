/**
 * Plugin tree views (contributes.treeViews): the re-validated views of
 * process plugins active in this workspace. Items are loaded on demand by
 * the sidebar panel and never persisted.
 */
import { ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import type { PluginTreeView, TreeViewIssue } from "@/domain/plugins/tree-views";

export const usePluginTreeViewsStore = defineStore("plugin-tree-views", () => {
  const views = ref<PluginTreeView[]>([]);
  const issues = ref<TreeViewIssue[]>([]);
  /** True once the consumer applied a snapshot. */
  const loaded = ref(false);
  /** Bumped on every snapshot so open panels reload their roots. */
  const revision = ref(0);

  function setContributions(next: PluginTreeView[], nextIssues: TreeViewIssue[]) {
    views.value = next;
    issues.value = nextIssues;
    loaded.value = true;
    revision.value += 1;
  }

  function clearContributions() {
    views.value = [];
    issues.value = [];
    revision.value += 1;
  }

  return { views, issues, loaded, revision, setContributions, clearContributions };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(usePluginTreeViewsStore, import.meta.hot));
}
