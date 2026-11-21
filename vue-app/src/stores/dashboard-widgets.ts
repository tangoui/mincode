/**
 * Plugin dashboard widgets (`dashboardWidgets` contribution point) and the
 * user's layout. Contributed widgets are runtime data set by
 * services/contributions/dashboard-widgets.ts; the layout (order + hidden) is
 * workspace-scoped runtime state (`app_dashboard_plugin_widgets_v1`, listed in
 * WORKSPACE_SCOPED_RUNTIME_STORAGE_KEYS so workspace deletion handles it).
 */
import { acceptHMRUpdate, defineStore } from "pinia";
import { computed, ref, shallowRef, watch } from "vue";
import { useWorkspaceScopedPersistence } from "@/composables/use-workspace-state-scope";
import type { PointContribution } from "@/domain/plugins/contributions";
import {
  DASHBOARD_WIDGETS_STORAGE_KEY,
  arrangeDashboardWidgets,
  emptyDashboardWidgetLayout,
  moveDashboardWidget,
  normalizeDashboardWidgetLayout,
  orderDashboardWidgets,
  resolveDashboardWidgets,
  setDashboardWidgetHidden,
  type DashboardWidget,
  type DashboardWidgetLayout,
} from "@/domain/plugins/dashboard-widgets";

export const useDashboardWidgetsStore = defineStore("dashboard-widgets", () => {
  const persistence = useWorkspaceScopedPersistence<DashboardWidgetLayout>(DASHBOARD_WIDGETS_STORAGE_KEY, { migrateLegacy: false });
  const widgets = shallowRef<readonly DashboardWidget[]>([]);
  const layout = ref<DashboardWidgetLayout>(emptyDashboardWidgetLayout());
  const loadedScope = ref<string | undefined>();

  const arranged = computed(() => arrangeDashboardWidgets(widgets.value, layout.value));
  const ordered = computed(() => orderDashboardWidgets(widgets.value, layout.value));
  const hiddenSet = computed(() => new Set(layout.value.hidden));

  function setContributions(contributions: readonly PointContribution[]) {
    widgets.value = resolveDashboardWidgets(contributions);
  }

  function clearContributions() {
    widgets.value = [];
  }

  async function hydrate() {
    const captured = persistence.capture();
    if (loadedScope.value === captured.scopeId) return;
    const stored = await persistence.load(captured);
    if (!persistence.isCurrent(captured)) return;
    layout.value = normalizeDashboardWidgetLayout(stored);
    loadedScope.value = captured.scopeId;
  }

  watch(persistence.scopeId, (scopeId) => {
    if (scopeId === loadedScope.value) return;
    layout.value = emptyDashboardWidgetLayout();
    loadedScope.value = undefined;
    void hydrate();
  });

  async function persist(next: DashboardWidgetLayout) {
    if (next === layout.value) return;
    const captured = persistence.capture();
    layout.value = next;
    await persistence.save(next, captured);
  }

  async function setHidden(key: string, hidden: boolean) {
    await hydrate();
    await persist(setDashboardWidgetHidden(layout.value, key, hidden));
  }

  async function move(key: string, delta: number) {
    await hydrate();
    await persist(moveDashboardWidget(widgets.value, layout.value, key, delta));
  }

  function isHidden(key: string) {
    return hiddenSet.value.has(key);
  }

  return { widgets, layout, arranged, ordered, setContributions, clearContributions, hydrate, setHidden, move, isHidden };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useDashboardWidgetsStore, import.meta.hot));
}
