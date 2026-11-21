import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";
import {
  normalizeAppLockAttempt,
  normalizeAppLockState,
  type AppLockAttemptResult,
  type AppLockPublicState,
} from "@/domain/security/app-lock";

/**
 * 锁屏的权威状态在 Go（backend/applock）：口令校验记录只存在后端，前端只拿到
 * 不含口令材料的公开状态。解锁/改口令/关闭都只提交口令明文，由后端用自己保存的
 * 记录校验，调用方无法“自带记录”，也无法把旧快照写回。
 */
function requireRuntime() {
  if (!isDesktopRuntime()) throw new Error("app lock requires the desktop runtime");
}

export async function loadAppLockState(): Promise<AppLockPublicState> {
  requireRuntime();
  return normalizeAppLockState(await invokeBackend<unknown>("app_lock_state", {}));
}

export async function lockApp(): Promise<AppLockPublicState> {
  requireRuntime();
  return normalizeAppLockState(await invokeBackend<unknown>("app_lock_lock", {}));
}

export async function unlockApp(passcode: string): Promise<AppLockAttemptResult> {
  requireRuntime();
  return normalizeAppLockAttempt(await invokeBackend<unknown>("app_lock_unlock", { passcode }));
}

/** 首次启用时 currentPasscode 留空；已启用时必须提供当前口令。 */
export async function setAppLockPasscode(
  passcode: string,
  currentPasscode = ""
): Promise<AppLockAttemptResult> {
  requireRuntime();
  return normalizeAppLockAttempt(
    await invokeBackend<unknown>("app_lock_set_passcode", { passcode, currentPasscode })
  );
}

export async function disableAppLock(currentPasscode: string): Promise<AppLockAttemptResult> {
  requireRuntime();
  return normalizeAppLockAttempt(await invokeBackend<unknown>("app_lock_disable", { currentPasscode }));
}

export async function setAppLockPreferences(preferences: {
  autoLockMinutes?: number;
  lockOnLaunch?: boolean;
}): Promise<AppLockPublicState> {
  requireRuntime();
  return normalizeAppLockState(await invokeBackend<unknown>("app_lock_set_preferences", preferences));
}
