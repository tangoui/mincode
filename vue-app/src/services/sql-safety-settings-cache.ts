/**
 * SQL 安全网设置的版本号：保存设置后递增，缓存设置的地方（AI 单行工具的 30 秒缓存）记下版本，
 * 读取缓存前比对，版本变了就重新读取。不依赖 桌面桥，AI 工具模块可以直接引用。
 */
let settingsVersion = 0;

/** 当前设置版本。 */
export function sqlSafetySettingsVersion(): number {
  return settingsVersion;
}

/** 让所有已缓存的安全网设置失效。 */
export function invalidateSqlSafetySettingsCaches(): void {
  settingsVersion += 1;
}
