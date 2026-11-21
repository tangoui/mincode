/**
 * 浏览器本地模式的 AI API Key：只保存在当前标签页的 sessionStorage（键 `mincode_browser_ai_keys_v1`，
 * 按 AI 配置档案 id），关闭标签页即清除；从不写入 localStorage。桌面端不使用本模块
 * （settings-persistence 只在浏览器本地模式调用它，桌面端密钥由 Go 加密存储）。
 *
 * 本模块不依赖桌面桥，体积很小，可被主包中的设置持久化直接导入。
 */

export const BROWSER_AI_KEYS_STORAGE_KEY = "mincode_browser_ai_keys_v1";

type SettingsRecord = Record<string, unknown>;

function sessionStore(): Storage | undefined {
  try {
    return globalThis.sessionStorage ?? undefined;
  } catch {
    // Access can throw when site data is blocked.
    return undefined;
  }
}

function readKeys(): Record<string, string> {
  try {
    const parsed: unknown = JSON.parse(sessionStore()?.getItem(BROWSER_AI_KEYS_STORAGE_KEY) || "{}");
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    return Object.fromEntries(
      Object.entries(parsed).filter((entry): entry is [string, string] => typeof entry[1] === "string" && entry[1] !== ""),
    );
  } catch {
    return {};
  }
}

function writeKeys(keys: Record<string, string>) {
  const storage = sessionStore();
  if (!storage) return;
  try {
    if (Object.keys(keys).length) storage.setItem(BROWSER_AI_KEYS_STORAGE_KEY, JSON.stringify(keys));
    else storage.removeItem(BROWSER_AI_KEYS_STORAGE_KEY);
  } catch {
    // Quota or blocked storage: the key then only lives in memory for this page.
  }
}

/** The API key this tab holds for an AI profile, or "". */
export function browserAiKey(profileId: string): string {
  return readKeys()[profileId] ?? "";
}

/** Stores (or, with an empty key, forgets) this tab's API key for an AI profile. */
export function setBrowserAiKey(profileId: string, key: string): void {
  const keys = readKeys();
  if (key) keys[profileId] = key;
  else delete keys[profileId];
  writeKeys(keys);
}

function profiles(settings: SettingsRecord): SettingsRecord[] {
  return Array.isArray(settings.aiProfiles)
    ? settings.aiProfiles.filter((profile): profile is SettingsRecord => typeof profile === "object" && profile !== null)
    : [];
}

/** Settings with each AI profile's blank `apiKey` filled from this tab's keys (input unchanged). */
export function withBrowserAiKeys<T extends SettingsRecord>(settings: T): T {
  const keys = readKeys();
  if (!Array.isArray(settings.aiProfiles) || !Object.keys(keys).length) return settings;
  return {
    ...settings,
    aiProfiles: settings.aiProfiles.map((profile: unknown) => {
      if (typeof profile !== "object" || profile === null) return profile;
      const record = profile as SettingsRecord;
      const key = typeof record.id === "string" ? keys[record.id] : undefined;
      return key && !record.apiKey ? { ...record, apiKey: key } : record;
    }),
  };
}

/** Keeps exactly the keys of the current AI profiles in this tab (removed profiles' keys are dropped). */
export function storeBrowserAiKeys(settings: SettingsRecord): void {
  const keys: Record<string, string> = {};
  for (const profile of profiles(settings)) {
    if (typeof profile.id === "string" && typeof profile.apiKey === "string" && profile.apiKey) keys[profile.id] = profile.apiKey;
  }
  writeKeys(keys);
}
