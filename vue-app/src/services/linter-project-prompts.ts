/**
 * Asks the user once per workspace whether to use a plugin linter the project
 * is set up for (contributes.linters[].detect). The trigger is opening a file
 * of one of the linter's languages, not opening the project. Accepting turns
 * the linter on for the languages that have none and, when the linter declares
 * an enableSetting (the tool runs project code), turns that setting on for the
 * current workspace. Detection itself never runs a linter.
 */
import {
  LINTER_PROMPTS_STORAGE_KEY,
  linterPromptPlan,
  normalizeLinterPrompts,
  recordLinterPrompt,
  type LinterPromptPlan,
  type LinterPrompts,
} from "@/domain/plugins/linter-prompts";
import type { PluginLinter } from "@/domain/plugins/linters";

/** How long one project's detection result is reused (new config files appear rarely). */
const DETECT_TTL_MS = 60_000;

export type LinterPromptDecision = "enable" | "dismiss" | "later";

export interface LinterPromptDependencies {
  /** Resolves once the saved per-language linter choices are loaded. */
  ready?(): Promise<void>;
  workspaceId(): string;
  /** Root announced to the plugin host; "" without a local project. */
  projectRoot(): string;
  linters(): readonly PluginLinter[];
  /** language → selected linter ref. */
  selections(): Record<string, string | undefined>;
  detect(): Promise<{ root: string; linters: string[] }>;
  /** Value of a plugin's boolean setting in the given workspace. */
  settingValue(pluginId: string, key: string, workspaceId: string): Promise<boolean>;
  loadPrompts(): Promise<unknown>;
  savePrompts(prompts: LinterPrompts): Promise<void>;
  /** Shows the dialog; "later" means it was closed without an answer. */
  confirm(plan: LinterPromptPlan<PluginLinter>): Promise<LinterPromptDecision>;
  selectLinter(language: string, ref: string): void;
  enableSetting(pluginId: string, key: string, workspaceId: string): Promise<void>;
  /** Lints open files of the linter again (after its setting changed). */
  relint(ref: string): void;
  reportError(error: unknown): void;
}

export function createLinterProjectPrompts(deps: LinterPromptDependencies) {
  let prompts: LinterPrompts | undefined;
  let detection: { root: string; at: number; linters: Promise<string[]> } | undefined;
  /** Linters closed without an answer in this session, per workspace. */
  const deferred = new Set<string>();
  let busy = false;

  async function loadPrompts(): Promise<LinterPrompts> {
    prompts ??= normalizeLinterPrompts(await deps.loadPrompts().catch(() => undefined));
    return prompts;
  }

  function detected(root: string): Promise<string[]> {
    const now = Date.now();
    if (!detection || detection.root !== root || now - detection.at > DETECT_TTL_MS) {
      const linters = deps.detect()
        .then((result) => (result.root ? result.linters : []))
        .catch(() => [] as string[]);
      detection = { root, at: now, linters };
    }
    return detection.linters;
  }

  /** Called when a project file of `language` is opened in the editor. */
  async function onModelOpened(language: string): Promise<void> {
    if (busy) return;
    const root = deps.projectRoot();
    const workspaceId = deps.workspaceId();
    const candidates = deps.linters().filter((linter) => linter.detect && linter.languages.includes(language));
    if (!root || !workspaceId || !candidates.length) return;
    busy = true;
    try {
      await deps.ready?.();
      const refs = await detected(root);
      if (!refs.length) return;
      const settings: Record<string, boolean> = {};
      for (const linter of candidates) {
        if (linter.enableSetting && refs.includes(linter.ref)) {
          settings[linter.ref] = await deps.settingValue(linter.pluginId, linter.enableSetting, workspaceId).catch(() => false);
        }
      }
      const answered = await loadPrompts();
      const plan = linterPromptPlan({
        language, linters: candidates.filter((linter) => !deferred.has(`${workspaceId}\n${linter.ref}`)),
        detected: refs, selections: deps.selections(), settings, prompts: answered, workspaceId,
      });
      if (!plan) return;
      const decision = await deps.confirm(plan);
      if (decision === "later") {
        deferred.add(`${workspaceId}\n${plan.linter.ref}`);
        return;
      }
      if (decision === "enable") {
        // The setting goes first so the first lint after the selection already sees it. If it
        // cannot be written the linter is still selected; the plugin then points to the setting.
        if (plan.enableSetting) {
          await deps.enableSetting(plan.linter.pluginId, plan.enableSetting, workspaceId).catch(deps.reportError);
        }
        for (const selected of plan.languages) deps.selectLinter(selected, plan.linter.ref);
        if (plan.enableSetting) deps.relint(plan.linter.ref);
      }
      prompts = recordLinterPrompt(await loadPrompts(), workspaceId, plan.linter.ref, decision === "enable" ? "enabled" : "dismissed");
      await deps.savePrompts(prompts);
    } catch (error) {
      deps.reportError(error);
    } finally {
      busy = false;
    }
  }

  return { onModelOpened };
}

export { LINTER_PROMPTS_STORAGE_KEY };
