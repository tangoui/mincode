/**
 * SQL 安全网设置的读取与保存（db_safety_settings_get / db_safety_settings_set）。
 * 保存成功后让缓存的设置失效（sql-safety-settings-cache）；确认框、撤回对话框每次都直接读后端，
 * 不需要失效。
 */
import { parseSqlSafetySettings, type SqlSafetySettings } from "@/domain/database/sql-safety";
import { invalidateSqlSafetySettingsCaches } from "@/services/sql-safety-settings-cache";
import { invokeBackend } from "@/services/desktop-bridge";

export async function loadSqlSafetySettings(): Promise<SqlSafetySettings> {
  return parseSqlSafetySettings(await invokeBackend<unknown>("db_safety_settings_get", {}));
}

/** 保存设置（后端再按同样范围校验，失败时抛出后端错误）；返回后端保存后的值。 */
export async function saveSqlSafetySettings(settings: SqlSafetySettings): Promise<SqlSafetySettings> {
  const saved = await invokeBackend<unknown>("db_safety_settings_set", { settings: { ...settings } });
  invalidateSqlSafetySettingsCaches();
  return parseSqlSafetySettings(saved);
}
