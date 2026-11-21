/**
 * Runtime of the `linters` contribution point consumer (process plugins
 * active in this workspace), loaded on demand by services/contributions/
 * linters.ts. It stores the re-validated linters and wires the Monaco
 * stage (services/monaco-linters.ts) to the store and i18n. Plugin linters
 * are off by default: document text reaches a linter only for languages
 * where the user selected it in Settings, and while typing only after the
 * separate per-language opt-in. Registering data never starts a plugin
 * process and never loads Monaco.
 */
import { effectScope, h, watch, type EffectScope } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { collectLinters, linterTitle, type PluginLinter } from "@/domain/plugins/linters";
import { LINTER_PROMPTS_STORAGE_KEY, type LinterPromptPlan } from "@/domain/plugins/linter-prompts";
import type { PointContribution } from "@/domain/plugins/contributions";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import {
  refreshMonacoLinters,
  relintMonacoLinter,
  setMonacoLinterRuntime,
  setMonacoLinters,
  type LinterFailure,
  type MonacoLinterRuntime,
} from "@/services/monaco-linters";
import { createLinterProjectPrompts, type LinterPromptDecision } from "@/services/linter-project-prompts";
import { getPluginConfiguration, setPluginConfiguration } from "@/services/plugin-configuration-service";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";
import { usePluginLintersStore } from "@/stores/plugin-linters";
import { useWorkbenchStore } from "@/stores/workbench";

function title(linter: PluginLinter): string {
  return linterTitle(linter, useVueI18n().localeChain.value);
}

function report(failure: LinterFailure) {
  const { t } = useVueI18n();
  const keys: Record<LinterFailure["code"], MessageKey> = {
    too_large: "pluginContrib.linters.tooLarge",
    timeout: "pluginContrib.linters.timeout",
    unavailable: "pluginContrib.linters.unavailable",
    failed: "pluginContrib.linters.failed",
  };
  // Plugin error text is untrusted; it is shown as plain text and bounded.
  // eslint-disable-next-line no-control-regex -- removing control characters is the point
  const detail = failure.detail.replace(/[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069]/g, " ").slice(0, 200);
  ElMessage({ type: "warning", message: t(keys[failure.code], { title: title(failure.linter), plugin: failure.linter.pluginName, detail }), grouping: true });
}

/** Host-written dialog: the plugin only contributes its validated titles. */
async function confirmLinterPrompt(plan: LinterPromptPlan<PluginLinter>): Promise<LinterPromptDecision> {
  const { t } = useVueI18n();
  const tool = title(plan.linter);
  const plugin = plan.linter.pluginName;
  const lines = [
    plan.languages.length
      ? t("pluginContrib.linters.promptLanguages", { tool, plugin, languages: plan.languages.join(", ") })
      : t("pluginContrib.linters.promptSetting", { tool }),
    ...(plan.languages.length ? [t("pluginContrib.linters.promptPrivacy")] : []),
    ...(plan.enableSetting ? [t("pluginContrib.linters.promptRunsCode", { tool })] : []),
    t("pluginContrib.linters.promptChangeLater"),
  ];
  try {
    await ElMessageBox.confirm(() => h("div", { class: "linter-project-prompt" }, lines.map((line) => h("p", line))), t("pluginContrib.linters.promptTitle", { tool }), {
      confirmButtonText: t("pluginContrib.linters.promptEnable"),
      cancelButtonText: t("pluginContrib.linters.promptDismiss"),
      distinguishCancelAndClose: true,
      closeOnClickModal: false,
      autofocus: false,
    });
    return "enable";
  } catch (action) {
    return action === "cancel" ? "dismiss" : "later";
  }
}

function projectRoot(): string { return ""; }

let projectPrompts: ReturnType<typeof createLinterProjectPrompts> | undefined;

/** The once-per-workspace "use this linter?" prompt (desktop only). */
function linterProjectPrompts() {
  const store = usePluginLintersStore();
  projectPrompts ??= createLinterProjectPrompts({
    ready: () => store.loadPreferences(),
    workspaceId: () => useWorkbenchStore().activeWorkspaceId,
    projectRoot,
    linters: () => store.linters,
    selections: () => Object.fromEntries(Object.entries(store.preferences.byLanguage).map(([language, preference]) => [language, preference?.linter])),
    detect: async () => {
      const raw = await invokeBackend<unknown>("plugin_host_linters_detect", {});
      const value = raw && typeof raw === "object" ? (raw as { root?: unknown; linters?: unknown }) : {};
      const linters = Array.isArray(value.linters) ? value.linters.filter((ref): ref is string => typeof ref === "string") : [];
      return { root: typeof value.root === "string" ? value.root : "", linters };
    },
    // The prompt's own workspace id, so reads and writes never depend on the host's notion of the active one.
    settingValue: async (pluginId, key, workspaceId) => (await getPluginConfiguration(pluginId, workspaceId)).effective[key] === true,
    loadPrompts: () => loadPersistedValue<unknown>(LINTER_PROMPTS_STORAGE_KEY),
    savePrompts: (prompts) => savePersistedValue(LINTER_PROMPTS_STORAGE_KEY, prompts),
    confirm: confirmLinterPrompt,
    selectLinter: (language, ref) => store.setPreference(language, { linter: ref }),
    enableSetting: async (pluginId, key, workspaceId) => {
      await setPluginConfiguration(pluginId, "workspace", { [key]: true }, workspaceId);
    },
    relint: relintMonacoLinter,
    reportError: (error) => {
      const { t } = useVueI18n();
      ElMessage({ type: "warning", message: t("pluginContrib.linters.promptFailed", { detail: String(error instanceof Error ? error.message : error).slice(0, 200) }), grouping: true });
    },
  });
  return projectPrompts;
}

function runtime(): MonacoLinterRuntime {
  const store = usePluginLintersStore();
  return {
    selected: (language) => store.selectedFor(language),
    preference: (language) => store.preferenceFor(language),
    projectRoots: () => [],
    sourceLabel: (linter) => `${title(linter)} (${linter.pluginName})`,
    report,
    modelOpened: (language) => {
      if (isDesktopRuntime()) void linterProjectPrompts().onModelOpened(language);
    },
  };
}

let wired = false;
let scope: EffectScope | undefined;

/**
 * Applies one snapshot of the point (loaded lazily by linters.ts only when a
 * plugin contributes linters). Returns the cleanup of this snapshot.
 */
export function applyLinterContributions(contributions: PointContribution[]): () => void {
  const store = usePluginLintersStore();
  const result = collectLinters(contributions);
  store.setContributions(result.linters, result.issues);
  if (!wired) {
    wired = true;
    void store.loadPreferences();
    setMonacoLinterRuntime(runtime());
    scope = effectScope(true);
    scope.run(() => {
      watch(() => store.preferences, () => refreshMonacoLinters());
    });
  }
  setMonacoLinters(result.linters);
  return () => {
    setMonacoLinters([]);
    store.clearContributions();
  };
}

/** Stops the Monaco stage wiring (the consumer was unregistered). */
export function stopLinterRuntime(): void {
  scope?.stop();
  scope = undefined;
  wired = false;
  setMonacoLinterRuntime(undefined);
}
