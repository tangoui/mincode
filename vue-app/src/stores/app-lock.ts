import { computed, ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import {
  APP_LOCK_STATE_EVENT,
  createDefaultAppLockSettings,
  isNewerAppLockState,
  normalizeAutoLockMinutes,
  shouldAutoLock,
  type AppLockAttemptResult,
  type AppLockPublicState,
} from "@/domain/security/app-lock";
import {
  disableAppLock,
  loadAppLockState,
  lockApp,
  setAppLockPasscode,
  setAppLockPreferences,
  unlockApp,
} from "@/services/app-lock-service";
import { isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";

/**
 * 每个 WebView 各有一份本 store，但权威状态只在 Go（backend/applock）：
 * - 口令校验记录从不进入前端，解锁/改口令/关闭只提交口令明文；
 * - 后端每次变更都会广播 app-lock:state，各窗口把它当作“有变化”的提示并重新拉取，
 *   不直接信任事件载荷（任何渲染进程都能发同名事件）；
 * - 只接受 revision 更新的状态，乱序到达的旧状态不会覆盖新状态。
 */
export const useAppLockStore = defineStore("app-lock", () => {
  const defaults = createDefaultAppLockSettings();
  const enabled = ref(false);
  const autoLockMinutes = ref(defaults.autoLockMinutes);
  const lockOnLaunch = ref(defaults.lockOnLaunch);
  const locked = ref(false);
  const revision = ref(-1);
  const hydrated = ref(false);
  const failedAttempts = ref(0);
  const retryAvailableAt = ref(0);
  const verifying = ref(false);

  // 闲置判定只关心「最后一次交互在什么时候」，不需要参与渲染，保持非响应式避免鼠标移动引发更新。
  let lastActivityAt = Date.now();
  let hydrationPromise: Promise<void> | null = null;
  let stopBroadcastListener: (() => void) | undefined;

  /** 浏览器预览没有 Go 后端，无法派生口令，锁屏在该环境下不可用。 */
  const available = computed(() => isDesktopRuntime());
  const configured = computed(() => enabled.value);

  function current(): AppLockPublicState | undefined {
    if (revision.value < 0) return undefined;
    return {
      enabled: enabled.value,
      autoLockMinutes: autoLockMinutes.value,
      lockOnLaunch: lockOnLaunch.value,
      locked: locked.value,
      revision: revision.value,
    };
  }

  function apply(next: AppLockPublicState) {
    if (!isNewerAppLockState(next, current())) return;
    const wasLocked = locked.value;
    enabled.value = next.enabled;
    autoLockMinutes.value = next.autoLockMinutes;
    lockOnLaunch.value = next.lockOnLaunch;
    locked.value = next.locked;
    revision.value = next.revision;
    hydrated.value = true;
    if (wasLocked && !next.locked) {
      failedAttempts.value = 0;
      retryAvailableAt.value = 0;
      noteActivity();
    } else if (!wasLocked && next.locked) {
      failedAttempts.value = 0;
      retryAvailableAt.value = 0;
    }
  }

  function applyAttempt(result: AppLockAttemptResult) {
    apply(result.state);
    if (result.ok) return;
    failedAttempts.value = result.failedAttempts;
    retryAvailableAt.value = result.retryAfterMs ? Date.now() + result.retryAfterMs : 0;
  }

  async function refresh() {
    apply(await loadAppLockState());
  }

  function ensureListening() {
    if (stopBroadcastListener || !isDesktopRuntime()) return;
    stopBroadcastListener = listenAppEvent<unknown>(APP_LOCK_STATE_EVENT, () => {
      void refresh().catch(() => {
        // 拉取失败时保留当前状态；下一次变更或操作会再次同步。
      });
    });
  }

  function dispose() {
    stopBroadcastListener?.();
    stopBroadcastListener = undefined;
  }

  async function hydrate() {
    if (hydrated.value) return;
    if (hydrationPromise) return hydrationPromise;
    hydrationPromise = (async () => {
      if (!available.value) {
        hydrated.value = true;
        return;
      }
      await refresh();
      noteActivity();
    })().finally(() => {
      hydrationPromise = null;
    });
    return hydrationPromise;
  }

  function noteActivity() {
    lastActivityAt = Date.now();
  }

  /** 由外部计时器周期调用；返回是否在本次判定中锁定。 */
  function evaluateAutoLock(now = Date.now()) {
    if (
      !shouldAutoLock({
        enabled: configured.value,
        locked: locked.value,
        autoLockMinutes: autoLockMinutes.value,
        lastActivityAt,
        now,
      })
    ) {
      return false;
    }
    lock();
    return true;
  }

  function lock() {
    if (!configured.value || locked.value) return false;
    // 锁定方向总是安全的：先在本窗口立即生效，再让后端落盘并通知其他窗口。
    locked.value = true;
    failedAttempts.value = 0;
    retryAvailableAt.value = 0;
    void lockApp().then(apply, () => {
      // 后端失败时本窗口保持锁定；解锁仍需经后端校验口令。
    });
    return true;
  }

  async function unlock(input: string) {
    if (!locked.value) return true;
    if (Date.now() < retryAvailableAt.value) return false;
    verifying.value = true;
    try {
      const result = await unlockApp(input);
      applyAttempt(result);
      if (!result.ok) return false;
      // 口令已由后端校验通过。后端本就是解锁状态时（例如本窗口的先行锁定未能落到后端）
      // revision 不变，apply 不会改动本窗口，这里按后端结果显式放行。
      if (!result.state.locked) {
        locked.value = false;
        noteActivity();
      }
      return !locked.value;
    } finally {
      verifying.value = false;
    }
  }

  async function enable(nextPasscode: string) {
    const result = await setAppLockPasscode(nextPasscode);
    applyAttempt(result);
    if (!result.ok) throw new Error("app lock is already enabled; the current passcode is required");
    noteActivity();
  }

  async function changePasscode(currentPasscode: string, nextPasscode: string) {
    const result = await setAppLockPasscode(nextPasscode, currentPasscode);
    applyAttempt(result);
    if (result.ok) noteActivity();
    return result.ok;
  }

  async function disable(currentPasscode: string) {
    const result = await disableAppLock(currentPasscode);
    applyAttempt(result);
    return result.ok;
  }

  async function setAutoLockMinutes(minutes: number) {
    apply(await setAppLockPreferences({ autoLockMinutes: normalizeAutoLockMinutes(minutes) }));
    noteActivity();
  }

  async function setLockOnLaunch(value: boolean) {
    apply(await setAppLockPreferences({ lockOnLaunch: value }));
  }

  return {
    enabled,
    autoLockMinutes,
    lockOnLaunch,
    locked,
    revision,
    hydrated,
    failedAttempts,
    retryAvailableAt,
    verifying,
    available,
    configured,
    hydrate,
    refresh,
    ensureListening,
    dispose,
    noteActivity,
    evaluateAutoLock,
    lock,
    unlock,
    enable,
    changePasscode,
    disable,
    setAutoLockMinutes,
    setLockOnLaunch,
  };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useAppLockStore, import.meta.hot));
}
