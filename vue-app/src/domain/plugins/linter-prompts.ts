/**
 * When to ask the user whether to use a plugin linter the current project is
 * set up for (contributes.linters[].detect). Detection only decides when to
 * ask; nothing runs until the user accepts. The answers are machine-local
 * (`mincode_plugin_linter_prompts_v1`, not an `app_` key, so config export and
 * sync skip them) and kept per workspace: a project is asked at most once per
 * linter, whatever the answer.
 */
import { parseLinterRef } from "./linters";

export const LINTER_PROMPTS_STORAGE_KEY = "mincode_plugin_linter_prompts_v1";
const WORKSPACES_MAX = 256;
const ANSWERS_PER_WORKSPACE_MAX = 64;
const WORKSPACE_ID_MAX = 128;

export type LinterPromptAnswer = "enabled" | "dismissed";

export interface LinterPrompts {
  version: 1;
  /** workspace id → linter ref → answer, oldest workspace first. */
  byWorkspace: Record<string, Record<string, LinterPromptAnswer>>;
}

/** The parts of a PluginLinter the prompt needs. */
export interface PromptableLinter {
  ref: string;
  pluginId: string;
  languages: string[];
  detect?: { files: string[] };
  enableSetting?: string;
}

export interface LinterPromptPlan<T extends PromptableLinter = PromptableLinter> {
  linter: T;
  /** Languages that have no linter yet and will be set to this one. */
  languages: string[];
  /** Present when the workspace setting still has to be turned on. */
  enableSetting?: string;
}

export function normalizeLinterPrompts(value: unknown): LinterPrompts {
  const prompts: LinterPrompts = { version: 1, byWorkspace: {} };
  const byWorkspace = value && typeof value === "object" ? (value as { byWorkspace?: unknown }).byWorkspace : undefined;
  if (!byWorkspace || typeof byWorkspace !== "object") return prompts;
  for (const [workspaceId, answers] of Object.entries(byWorkspace as Record<string, unknown>).slice(-WORKSPACES_MAX)) {
    if (!workspaceId || workspaceId.length > WORKSPACE_ID_MAX || !answers || typeof answers !== "object") continue;
    const kept: Record<string, LinterPromptAnswer> = {};
    for (const [ref, answer] of Object.entries(answers as Record<string, unknown>).slice(-ANSWERS_PER_WORKSPACE_MAX)) {
      if (parseLinterRef(ref) && (answer === "enabled" || answer === "dismissed")) kept[ref] = answer;
    }
    if (Object.keys(kept).length) prompts.byWorkspace[workspaceId] = kept;
  }
  return prompts;
}

/** Returns new prompts with the answer recorded; the workspace becomes the newest. */
export function recordLinterPrompt(prompts: LinterPrompts, workspaceId: string, ref: string, answer: LinterPromptAnswer): LinterPrompts {
  const byWorkspace = { ...prompts.byWorkspace };
  const answers = { ...(byWorkspace[workspaceId] ?? {}) };
  delete byWorkspace[workspaceId];
  delete answers[ref];
  answers[ref] = answer;
  byWorkspace[workspaceId] = Object.fromEntries(Object.entries(answers).slice(-ANSWERS_PER_WORKSPACE_MAX));
  return { version: 1, byWorkspace: Object.fromEntries(Object.entries(byWorkspace).slice(-WORKSPACES_MAX)) };
}

export interface LinterPromptInput<T extends PromptableLinter> {
  /** Language of the file the user just opened. */
  language: string;
  linters: readonly T[];
  /** Refs the host detected in the current project. */
  detected: readonly string[];
  /** language → selected linter ref (machine-local preference). */
  selections: Readonly<Record<string, string | undefined>>;
  /** linter ref → current workspace value of its enableSetting. */
  settings: Readonly<Record<string, boolean | undefined>>;
  prompts: LinterPrompts;
  workspaceId: string;
}

/**
 * The first detected, not yet answered linter for `language` that still needs
 * something: a language without any linter, or its workspace setting. A
 * language that already uses another linter is never taken over.
 */
export function linterPromptPlan<T extends PromptableLinter>(input: LinterPromptInput<T>): LinterPromptPlan<T> | undefined {
  const answered = input.prompts.byWorkspace[input.workspaceId] ?? {};
  for (const linter of input.linters) {
    if (!linter.detect || !linter.languages.includes(input.language) || !input.detected.includes(linter.ref) || answered[linter.ref]) continue;
    const current = input.selections[input.language];
    if (current && current !== linter.ref) continue;
    const languages = linter.languages.filter((language) => !input.selections[language]);
    const needsSetting = Boolean(linter.enableSetting) && input.settings[linter.ref] !== true;
    if (!languages.length && !needsSetting) continue;
    return needsSetting ? { linter, languages, enableSetting: linter.enableSetting } : { linter, languages };
  }
  return undefined;
}
