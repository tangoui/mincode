import { acceptHMRUpdate, defineStore } from "pinia";
import { ref } from "vue";

/**
 * Changed-file counts reported by each mounted project workspace, keyed by its
 * workspace state scope, so the activity bar can badge the source-control entry.
 */
export const useSourceControlBadgeStore = defineStore("source-control-badge", () => {
  const counts = ref<Record<string, number>>({});

  function report(scopeId: string, count: number) {
    if (count > 0) counts.value = { ...counts.value, [scopeId]: count };
    else clear(scopeId);
  }

  function clear(scopeId: string) {
    if (!(scopeId in counts.value)) return;
    const next = { ...counts.value };
    delete next[scopeId];
    counts.value = next;
  }

  function countFor(scopeId: string) {
    return counts.value[scopeId] ?? 0;
  }

  return { counts, report, clear, countFor };
});

if (import.meta.hot) import.meta.hot.accept(acceptHMRUpdate(useSourceControlBadgeStore, import.meta.hot));
