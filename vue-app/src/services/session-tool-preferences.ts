import {
  builtinRootsPayload,
  normalizeSessionToolPrefs,
  type SessionToolPrefs,
} from "@/domain/ai/session-tools";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

export const SESSION_TOOLS_KEY = "app_ai_session_tools_v1";
export const SESSION_TOOLS_EVENT = "app:session-tools-changed";

export async function loadSessionToolPrefs(): Promise<SessionToolPrefs> {
  try {
    return normalizeSessionToolPrefs(await loadPersistedValue<unknown>(SESSION_TOOLS_KEY));
  } catch {
    return normalizeSessionToolPrefs(null);
  }
}

export async function saveSessionToolPrefs(value: SessionToolPrefs): Promise<SessionToolPrefs> {
  const normalized = normalizeSessionToolPrefs(value);
  await savePersistedValue(SESSION_TOOLS_KEY, normalized);
  window.dispatchEvent(new CustomEvent<SessionToolPrefs>(SESSION_TOOLS_EVENT, { detail: normalized }));
  return normalized;
}

/**
 * 把内置工具的数据目录覆盖同步到后端（整表替换），返回后端解析后的绝对路径。
 * 校验失败时后端保持原有覆盖不变，错误抛给调用方提示。
 */
export async function applySessionToolRoots(prefs: SessionToolPrefs): Promise<Record<string, string>> {
  if (!isDesktopRuntime()) return {};
  const resolved = await invokeBackend<Record<string, string> | null>("set_ai_session_roots", {
    roots: builtinRootsPayload(prefs),
  });
  return resolved ?? {};
}

/** 只读调用方（例如仪表盘）在列出会话前同步一次目录覆盖，失败时静默回退到默认目录。 */
export async function syncSessionToolRoots(): Promise<Record<string, string>> {
  try {
    return await applySessionToolRoots(await loadSessionToolPrefs());
  } catch {
    return {};
  }
}
