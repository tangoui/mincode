/**
 * Runtime of the `formatters` contribution point consumer (process plugins
 * active in this workspace), loaded on demand by services/contributions/
 * formatters.ts only when a plugin contributes formatters. It stores the
 * re-validated formatters and wires the Monaco stage
 * (services/monaco-formatters.ts) to the store, i18n and the picker dialog.
 * Built-in formatting stays the default: a plugin formatter runs only after
 * the user picks it ("Format Document With…", the settings section, or the
 * one-time prompt when a language has only plugin formatters), and format on
 * save is a separate per-language opt-in. Registering data never starts a
 * plugin process and never loads Monaco.
 */
import { effectScope, h, watch, type EffectScope } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import type { PointContribution } from "@/domain/plugins/contributions";
import { collectFormatters, formatterTitle, type PluginFormatter } from "@/domain/plugins/formatters";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import {
  refreshMonacoFormatters,
  setMonacoFormatterRuntime,
  setMonacoFormatters,
  type FormatterFailure,
  type FormatterPickChoice,
  type MonacoFormatterRuntime,
} from "@/services/monaco-formatters";
import { useLocalProjectStore } from "@/stores/local-project";
import { usePluginFormattersStore } from "@/stores/plugin-formatters";

const BUILTIN_CHOICE = "builtin";

function title(formatter: PluginFormatter): string {
  return formatterTitle(formatter, useVueI18n().localeChain.value);
}

async function pick(options: {
  language: string;
  formatters: PluginFormatter[];
  current?: string;
  builtin: boolean;
}): Promise<FormatterPickChoice | undefined> {
  const { t } = useVueI18n();
  const { default: FormatterPickerDialog } = await import("@/components/projects/FormatterPickerDialog.vue");
  const choices = [
    ...(options.builtin ? [{ value: BUILTIN_CHOICE, label: t("pluginContrib.formatters.builtin") }] : []),
    ...options.formatters.map((formatter) => ({ value: formatter.ref, label: title(formatter), plugin: formatter.pluginName })),
  ];
  if (!choices.length) return undefined;
  const initial = options.current && choices.some((choice) => choice.value === options.current) ? options.current : choices[0].value;
  let selected = { choice: initial, remember: false };
  try {
    await ElMessageBox.confirm(
      h(FormatterPickerDialog, {
        language: options.language,
        options: choices,
        initial,
        onChange: (value: { choice: string; remember: boolean }) => {
          selected = value;
        },
      }),
      t("pluginContrib.formatters.formatWith"),
      {
        confirmButtonText: t("common.confirm"),
        cancelButtonText: t("common.cancel"),
        distinguishCancelAndClose: true,
        autofocus: false,
      },
    );
  } catch {
    return undefined;
  }
  if (selected.choice === BUILTIN_CHOICE) return { kind: "builtin", remember: selected.remember };
  return { kind: "plugin", ref: selected.choice, remember: selected.remember };
}

function report(failure: FormatterFailure) {
  const { t } = useVueI18n();
  const params = { title: title(failure.formatter), plugin: failure.formatter.pluginName };
  const keys: Record<FormatterFailure["code"], MessageKey> = {
    too_large: "pluginContrib.formatters.tooLarge",
    timeout: "pluginContrib.formatters.timeout",
    changed: "pluginContrib.formatters.changedDuringFormat",
    unavailable: "pluginContrib.formatters.unavailable",
    failed: "pluginContrib.formatters.failed",
  };
  const key = keys[failure.code];
  // Plugin error text is untrusted; it is shown as plain text and bounded.
  // eslint-disable-next-line no-control-regex -- removing control characters is the point
  const detail = failure.detail.replace(/[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069]/g, " ").slice(0, 200);
  ElMessage({ type: "warning", message: t(key, { ...params, detail }), grouping: true });
}

function runtime(): MonacoFormatterRuntime {
  const store = usePluginFormattersStore();
  return {
    preferredRef: (language) => store.selectedFor(language)?.ref,
    savePreferredRef: (language) => store.preferenceFor(language)?.formatter,
    formatOnSave: (language) => (store.preferenceFor(language)?.formatOnSave ? store.selectedFor(language) : undefined),
    projectRoots: () => {
      try {
        return useLocalProjectStore().projects.map((project) => project.root);
      } catch {
        return [];
      }
    },
    displayName: (formatter) => `${title(formatter)} (${formatter.pluginId})`,
    formatWithLabel: () => useVueI18n().t("pluginContrib.formatters.formatWith"),
    pick,
    remember: (language, ref) => store.setPreference(language, { formatter: ref }),
    report,
  };
}

let wired = false;
let scope: EffectScope | undefined;

/**
 * Applies one snapshot of the point (loaded lazily by formatters.ts only when
 * a plugin contributes formatters). Returns the cleanup of this snapshot.
 */
export function applyFormatterContributions(contributions: PointContribution[]): () => void {
  const store = usePluginFormattersStore();
  const result = collectFormatters(contributions);
  store.setContributions(result.formatters, result.issues);
  void ensureFormatterSaveRuntime();
  setMonacoFormatters(result.formatters);
  return () => {
    setMonacoFormatters([]);
    store.clearContributions();
  };
}

/** Saving must also honor unavailable stored selections when no plugin snapshot was loaded. */
export async function ensureFormatterSaveRuntime(): Promise<void> {
  const store = usePluginFormattersStore();
  if (!wired) {
    wired = true;
    setMonacoFormatterRuntime(runtime());
    scope = effectScope(true);
    scope.run(() => {
      const { localeChain } = useVueI18n();
      watch(localeChain, () => refreshMonacoFormatters());
    });
  }
  await store.loadPreferences();
}

/** Stops the Monaco stage wiring (the consumer was unregistered). */
export function stopFormatterRuntime(): void {
  scope?.stop();
  scope = undefined;
  wired = false;
  setMonacoFormatterRuntime(undefined);
}
