import { computed, ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import {
  UPDATE_CHECK_INITIAL_DELAY_MS,
  UPDATE_CHECK_TICK_MS,
  UPDATE_PREFERENCES_KEY,
  isAutoCheckDue,
  normalizeUpdatePreferences,
  pendingUpdateVersion,
  shouldNotifyUpdate,
  type UpdatePreferences,
} from "@/domain/update/auto-check";
import { useVueI18n } from "@/i18n/messages";
import { notifyApp } from "@/services/notifications";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";
import { checkForUpdates, type UpdateInfo } from "@/services/settings-service";
import { isDesktopRuntime } from "@/services/desktop-bridge";

export const useAppUpdateStore = defineStore("app-update", () => {
  const { t } = useVueI18n();
  const preferences = ref<UpdatePreferences>(normalizeUpdatePreferences(undefined));
  const info = ref<UpdateInfo>();
  const checking = ref(false);
  const error = ref("");
  const hydrated = ref(false);
  let hydrationPromise: Promise<void> | undefined;
  let inflight: Promise<UpdateInfo | undefined> | undefined;
  let startTimer: ReturnType<typeof setTimeout> | undefined;
  let tickTimer: ReturnType<typeof setInterval> | undefined;

  const autoCheck = computed(() => preferences.value.autoCheck);
  const skippedVersion = computed(() => preferences.value.skippedVersion);
  const lastCheckedAt = computed(() => preferences.value.lastCheckedAt);
  /** Version to flag on the settings entry points; undefined when there is nothing to act on. */
  const pendingVersion = computed(() => pendingUpdateVersion(info.value, preferences.value));

  async function hydrate() {
    if (hydrated.value) return;
    if (!hydrationPromise) {
      hydrationPromise = (async () => {
        try {
          preferences.value = normalizeUpdatePreferences(await loadPersistedValue<unknown>(UPDATE_PREFERENCES_KEY));
        } finally {
          hydrated.value = true;
        }
      })();
    }
    await hydrationPromise;
  }

  async function savePreferences(next: Partial<UpdatePreferences>) {
    preferences.value = normalizeUpdatePreferences({ ...preferences.value, ...next });
    await savePersistedValue(UPDATE_PREFERENCES_KEY, preferences.value).catch(() => undefined);
  }

  async function runCheck(manual: boolean): Promise<UpdateInfo | undefined> {
    await hydrate();
    checking.value = true;
    if (manual) error.value = "";
    try {
      const result = await checkForUpdates();
      info.value = result;
      error.value = "";
      const notify = !manual && shouldNotifyUpdate(result, preferences.value);
      await savePreferences({
        lastCheckedAt: Date.now(),
        ...(notify ? { notifiedVersion: result.version } : {}),
      });
      if (notify) {
        notifyApp({
          source: "update",
          level: "info",
          title: t("settings.update.available", { version: result.version }),
          message: t("notifications.update.availableHint"),
        });
      }
      return result;
    } catch (cause) {
      // Background checks stay silent (offline, captive portals); manual checks surface the error.
      if (manual) throw cause;
      return undefined;
    } finally {
      checking.value = false;
    }
  }

  /** Manual check from Settings; rejects so the caller can show the failure. */
  async function checkNow(): Promise<UpdateInfo> {
    if (inflight) await inflight.catch(() => undefined);
    const task = runCheck(true);
    inflight = task;
    try {
      return (await task)!;
    } catch (cause) {
      error.value = cause instanceof Error ? cause.message : String(cause);
      throw cause;
    } finally {
      if (inflight === task) inflight = undefined;
    }
  }

  async function checkIfDue() {
    if (inflight || !isDesktopRuntime()) return;
    await hydrate();
    if (!isAutoCheckDue(preferences.value, Date.now())) return;
    const task = runCheck(false);
    inflight = task;
    try {
      await task;
    } finally {
      if (inflight === task) inflight = undefined;
    }
  }

  function startAutoCheck() {
    if (startTimer || tickTimer || !isDesktopRuntime()) return;
    startTimer = setTimeout(() => {
      startTimer = undefined;
      void checkIfDue();
      tickTimer = setInterval(() => void checkIfDue(), UPDATE_CHECK_TICK_MS);
    }, UPDATE_CHECK_INITIAL_DELAY_MS);
  }

  async function setAutoCheck(enabled: boolean) {
    await hydrate();
    await savePreferences({ autoCheck: enabled });
    if (enabled) void checkIfDue();
  }

  async function skipVersion(version: string) {
    await hydrate();
    await savePreferences({ skippedVersion: version });
  }

  async function clearSkippedVersion() {
    await hydrate();
    await savePreferences({ skippedVersion: undefined });
  }

  return {
    info,
    checking,
    error,
    hydrated,
    autoCheck,
    skippedVersion,
    lastCheckedAt,
    pendingVersion,
    hydrate,
    checkNow,
    checkIfDue,
    startAutoCheck,
    setAutoCheck,
    skipVersion,
    clearSkippedVersion,
  };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useAppUpdateStore, import.meta.hot));
}
