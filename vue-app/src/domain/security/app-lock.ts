/**
 * 应用锁屏的纯逻辑：持久化形状、口令强度校验、自动锁定判定与失败退避。
 *
 * 口令本身不在这里出现——派生与比对由 Go 侧 `passcode_hash` / `passcode_verify`
 * 完成，本模块只描述可持久化的校验材料与时间/次数相关的判定。
 */

/** 由后端独占读写（backend/applock），通用 store_* 命令会拒绝该键。 */
export const APP_LOCK_STORAGE_KEY = "app_lock_v1";
/** 主窗口与弹出窗口之间同步锁定状态的事件名。 */
export const APP_LOCK_STATE_EVENT = "app-lock:state";

export const APP_LOCK_MIN_PASSCODE_LENGTH = 4;
export const APP_LOCK_MAX_PASSCODE_LENGTH = 128;
/** 0 表示只手动锁屏，不做闲置自动锁定。 */
export const APP_LOCK_AUTO_LOCK_CHOICES = [0, 1, 5, 10, 15, 30, 60, 120] as const;
export const DEFAULT_APP_LOCK_AUTO_LOCK_MINUTES = 10;
export const MAX_APP_LOCK_AUTO_LOCK_MINUTES = 720;
/** 前几次输错不惩罚，避免正常手误也要等待。 */
export const APP_LOCK_FREE_ATTEMPTS = 3;
export const APP_LOCK_MAX_BACKOFF_MS = 60_000;

export interface AppLockPasscodeRecord {
  algorithm: "pbkdf2-sha256";
  iterations: number;
  salt: string;
  hash: string;
}

export interface AppLockSettings {
  enabled: boolean;
  passcode?: AppLockPasscodeRecord;
  /** 闲置多少分钟后自动锁定；0 表示关闭。 */
  autoLockMinutes: number;
  /** 应用启动时是否直接进入锁屏。 */
  lockOnLaunch: boolean;
  /** 上次退出时是否处于锁定状态，保证强退后不会绕过锁屏。 */
  locked: boolean;
}

/** 后端（backend/applock）下发的公开状态：不含任何口令材料，revision 单调递增。 */
export interface AppLockPublicState {
  enabled: boolean;
  autoLockMinutes: number;
  lockOnLaunch: boolean;
  locked: boolean;
  revision: number;
}

/** 需要校验口令的操作结果；失败退避由后端统一计数。 */
export interface AppLockAttemptResult {
  ok: boolean;
  failedAttempts: number;
  retryAfterMs: number;
  state: AppLockPublicState;
}

export type AppLockPasscodeIssue = "empty" | "too-short" | "too-long" | "mismatch";

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function isAppLockPasscodeRecord(value: unknown): value is AppLockPasscodeRecord {
  if (!isRecord(value)) return false;
  return (
    value.algorithm === "pbkdf2-sha256" &&
    typeof value.iterations === "number" &&
    Number.isInteger(value.iterations) &&
    value.iterations > 0 &&
    typeof value.salt === "string" &&
    value.salt.length > 0 &&
    typeof value.hash === "string" &&
    value.hash.length > 0
  );
}

export function normalizeAutoLockMinutes(value: unknown): number {
  const minutes = typeof value === "number" ? value : Number(value);
  // 读不懂的值回落到默认间隔，而不是 0：0 等于悄悄关掉自动锁屏。
  if (!Number.isFinite(minutes)) return DEFAULT_APP_LOCK_AUTO_LOCK_MINUTES;
  if (minutes <= 0) return 0;
  return Math.min(Math.round(minutes), MAX_APP_LOCK_AUTO_LOCK_MINUTES);
}

export function createDefaultAppLockSettings(): AppLockSettings {
  return {
    enabled: false,
    autoLockMinutes: DEFAULT_APP_LOCK_AUTO_LOCK_MINUTES,
    lockOnLaunch: true,
    locked: false,
  };
}

export function normalizeAppLockSettings(value: unknown): AppLockSettings {
  const defaults = createDefaultAppLockSettings();
  if (!isRecord(value)) return defaults;
  const passcode = isAppLockPasscodeRecord(value.passcode) ? value.passcode : undefined;
  // 没有可用的口令记录时，锁必须视为关闭：否则一份损坏的配置会把用户永久挡在门外。
  const enabled = value.enabled === true && Boolean(passcode);
  return {
    enabled,
    passcode,
    autoLockMinutes: isRecord(value) && "autoLockMinutes" in value
      ? normalizeAutoLockMinutes(value.autoLockMinutes)
      : defaults.autoLockMinutes,
    lockOnLaunch: value.lockOnLaunch !== false,
    locked: enabled && value.locked === true,
  };
}

function nonNegativeInteger(value: unknown) {
  return typeof value === "number" && Number.isFinite(value) && value > 0 ? Math.floor(value) : 0;
}

export function normalizeAppLockState(value: unknown): AppLockPublicState {
  if (!isRecord(value)) throw new Error("app lock returned an unusable state");
  const enabled = value.enabled === true;
  return {
    enabled,
    autoLockMinutes: normalizeAutoLockMinutes(value.autoLockMinutes),
    lockOnLaunch: value.lockOnLaunch !== false,
    locked: enabled && value.locked === true,
    revision: nonNegativeInteger(value.revision),
  };
}

export function normalizeAppLockAttempt(value: unknown): AppLockAttemptResult {
  if (!isRecord(value)) throw new Error("app lock returned an unusable result");
  return {
    ok: value.ok === true,
    failedAttempts: nonNegativeInteger(value.failedAttempts),
    retryAfterMs: nonNegativeInteger(value.retryAfterMs),
    state: normalizeAppLockState(value.state),
  };
}

/**
 * 只接受严格更新的状态。事件与命令返回可能乱序到达；按 revision 取舍能保证
 * 每个窗口最终都收敛到后端的最新状态。同号状态也不接受：窗口本地先行锁定后，
 * 一个在途的、同 revision 的“未锁定”快照不能把它重新解开。
 */
export function isNewerAppLockState(
  next: AppLockPublicState,
  current: AppLockPublicState | undefined
) {
  return !current || next.revision > current.revision;
}

/** 决定 hydrate 之后应用是否应当处于锁屏：启动即锁，或上次退出时就锁着。 */
export function initialLockState(settings: AppLockSettings): boolean {
  if (!settings.enabled || !settings.passcode) return false;
  return settings.lockOnLaunch || settings.locked;
}

export function validateAppLockPasscode(
  passcode: string,
  confirmation?: string
): AppLockPasscodeIssue | null {
  if (!passcode) return "empty";
  if (passcode.length < APP_LOCK_MIN_PASSCODE_LENGTH) return "too-short";
  if (passcode.length > APP_LOCK_MAX_PASSCODE_LENGTH) return "too-long";
  if (confirmation !== undefined && passcode !== confirmation) return "mismatch";
  return null;
}

/** 连续输错后的等待时长：前三次不罚，其后指数退避并封顶。 */
export function unlockBackoffMs(failedAttempts: number): number {
  const penalized = Math.floor(failedAttempts) - APP_LOCK_FREE_ATTEMPTS;
  if (penalized <= 0) return 0;
  return Math.min(APP_LOCK_MAX_BACKOFF_MS, 1000 * 2 ** (penalized - 1));
}

export function shouldAutoLock(input: {
  enabled: boolean;
  locked: boolean;
  autoLockMinutes: number;
  lastActivityAt: number;
  now: number;
}): boolean {
  if (!input.enabled || input.locked || input.autoLockMinutes <= 0) return false;
  return input.now - input.lastActivityAt >= input.autoLockMinutes * 60_000;
}
