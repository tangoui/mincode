import { ElMessageBox } from "element-plus";
import { useVueI18n } from "@/i18n/messages";

/**
 * 导出连接配置前的口令确认。
 * 明文口令一旦落盘就脱离应用的加密存储，因此三个导出入口共用同一次显式确认，
 * 并把「包含 / 不包含」做成两个平级选项，关闭对话框则整体取消。
 */
export type SecretExportChoice = "with-secrets" | "without-secrets";

export async function confirmSecretExport(): Promise<SecretExportChoice | null> {
  const { t } = useVueI18n();
  try {
    await ElMessageBox.confirm(t("security.secretExport.message"), t("security.secretExport.title"), {
      confirmButtonText: t("security.secretExport.include"),
      cancelButtonText: t("security.secretExport.exclude"),
      confirmButtonClass: "el-button--danger",
      distinguishCancelAndClose: true,
      type: "warning",
    });
    return "with-secrets";
  } catch (action) {
    return action === "cancel" ? "without-secrets" : null;
  }
}

/** 导入一份含明文口令的文件时提醒用户凭据会被写入本机加密存储。 */
export async function confirmSecretImport(): Promise<boolean> {
  const { t } = useVueI18n();
  try {
    await ElMessageBox.confirm(t("security.secretImport.message"), t("security.secretImport.title"), {
      confirmButtonText: t("security.secretImport.confirm"),
      cancelButtonText: t("common.cancel"),
      type: "warning",
    });
    return true;
  } catch {
    return false;
  }
}
