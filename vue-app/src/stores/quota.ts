import { ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import { ElMessage } from "element-plus";
import { evaluateQuota, type QuotaResource } from "@/domain/workspace/quota";
import { fetchEffectiveQuota, type EffectiveQuota } from "@/services/cloud-companion-service";
import { isDesktopRuntime } from "@/services/desktop-bridge";
import { useVueI18n } from "@/i18n/messages";
import { scheduleIdleTask } from "@/services/idle-task";

const REFRESH_INTERVAL_MS = 30 * 60 * 1000;
const QUOTA_MESSAGES = {
  workspace: "quota.workspaceLimit",
  tab: "quota.tabLimit",
  plugin: "quota.pluginLimit",
} as const satisfies Record<QuotaResource, string>;

/** Workspace/tab limits resolved by the Go backend from the cloud entitlement (or the free policy). */
export const useQuotaStore = defineStore("quota", () => {
  const { t } = useVueI18n();
  const effective = ref<EffectiveQuota | null>(null);
  let inflight: Promise<void> | undefined;
  let timer: ReturnType<typeof setInterval> | undefined;

  function refresh() {
    if (!isDesktopRuntime()) return Promise.resolve();
    inflight ??= fetchEffectiveQuota()
      .then((result) => {
        effective.value = result;
      })
      // A failed refresh keeps the last resolved limits; the backend already applies offline rules.
      .catch(() => undefined)
      .finally(() => {
        inflight = undefined;
      });
    return inflight;
  }

  function start() {
    if (timer || typeof window === "undefined") return;
    // The lookup may hit the network; keep it off startup. Unknown limits admit everything meanwhile.
    scheduleIdleTask(() => void refresh(), 3000);
    timer = setInterval(() => void refresh(), REFRESH_INTERVAL_MS);
  }

  /** Returns whether one more resource may be created, and tells the user why when it may not. */
  function admit(resource: QuotaResource, current: number) {
    const decision = evaluateQuota(effective.value?.limits, resource, current);
    if (!decision.allowed) {
      const plan = t(`settings.pro.cloudPlan.${effective.value?.plan ?? "free"}` as Parameters<typeof t>[0]);
      ElMessage.warning({
        message: t(decision.forbidden ? "quota.pluginsNotAllowed" : QUOTA_MESSAGES[resource], { plan, limit: decision.limit }),
        grouping: true,
        duration: 5000,
        // At the top it covers the tab strip the user must use to close tabs, and hovering it pauses the timer.
        placement: "bottom",
        showClose: true,
      });
    }
    return decision.allowed;
  }

  start();
  return { effective, refresh, admit };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useQuotaStore, import.meta.hot));
}
