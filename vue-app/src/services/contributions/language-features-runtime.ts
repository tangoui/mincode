/**
 * App wiring of the `languageFeatures` Monaco stage that needs the UI:
 * registering the stage's code action Monaco command and telling the user
 * when a picked quick fix was refused in a read-only editor. (A CodeLens
 * click runs through services/plugin-code-lens-commands.ts.) Loaded lazily
 * by services/contributions/language-features.ts.
 */
import { ElMessage } from "element-plus";
import { useVueI18n } from "@/i18n/messages";
import type { MonacoLanguageFeatureRuntime } from "@/services/monaco-language-features";
import { registerMonacoCommand } from "@/services/monaco-commands";

export const languageFeatureCommandRuntime: Pick<MonacoLanguageFeatureRuntime, "registerEditorCommand" | "notifyReadOnly"> = {
  registerEditorCommand: registerMonacoCommand,
  notifyReadOnly: () => {
    ElMessage.warning(useVueI18n().t("pluginContrib.languageFeatures.readOnly"));
  },
};
