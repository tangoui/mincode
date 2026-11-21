/**
 * Monaco stage of the `languages` contribution point. Holds the accepted
 * plugin languages (services/contributions/languages.ts feeds it) and, once
 * Monaco has loaded, registers each new language id with its aliases only,
 * then its Monarch tokenizer and language configuration after the timed regex
 * probe passes. Associations (extensions, filenames, patterns) never reach
 * Monaco: MinCode keeps them here and editors resolve them through
 * `detectMonacoEditorLanguage`, after built-in detection, so built-in ids and
 * built-in associations win. The one exception is a plugin compound suffix
 * pattern (`*.blade.php`) that is more specific than the built-in extension
 * (`.php`); see `detectCompoundSuffixLanguage`.
 *
 * Monaco cannot unregister a language: a removed plugin language keeps its id
 * registered (untokenized, with no associations) while its tokenizer and
 * configuration are disposed. Reconciliation runs in a microtask, so the
 * consumer's dispose-then-apply collapses into one step, and every change
 * dispatches `PLUGIN_LANGUAGES_CHANGED_EVENT` so open editors re-detect.
 * Monaco is reached only through `registerMonacoContributionHook`, so
 * importing this module never loads it.
 */
import type { LanguagePluginIssue, PluginLanguage } from "@/domain/editor/language-contributions";
import { resolvePluginLanguageAssociations } from "@/domain/editor/language-contributions";
import {
  detectCompoundSuffixLanguage,
  detectProjectEditorLanguage,
  type ProjectEditorLanguageDefinition,
} from "@/domain/projects/editor-language";
import { registerMonacoContributionHook, type MonacoApi } from "@/services/monaco-contributions";
import { probeRegexes } from "@/services/regex-probe";

import type { languages } from "@/services/monaco-runtime.js";

/** Dispatched on `window` whenever plugin languages or associations change. */
export const PLUGIN_LANGUAGES_CHANGED_EVENT = "mincode:plugin-languages-changed";

type Disposable = { dispose(): void };

interface AppliedLanguage {
  fingerprint: string;
  disposers: Disposable[];
  issues: LanguagePluginIssue[];
}

let desired: readonly PluginLanguage[] = [];
let active: MonacoApi | undefined;
let unregisterHook: (() => void) | undefined;
let scheduled = false;
/** Ids MinCode registered with Monaco (they stay registered for the session). */
const registeredIds = new Set<string>();
const applied = new Map<string, AppliedLanguage>();
let associations: ProjectEditorLanguageDefinition[] = [];
let stageIssues: LanguagePluginIssue[] = [];

function notify() {
  if (typeof window === "undefined" || typeof window.dispatchEvent !== "function") return;
  window.dispatchEvent(new CustomEvent(PLUGIN_LANGUAGES_CHANGED_EVENT));
}

function schedule() {
  if (scheduled) return;
  scheduled = true;
  queueMicrotask(() => {
    scheduled = false;
    reconcile();
    notify();
  });
}

function builtInLanguages(monaco: MonacoApi): languages.ILanguageExtensionPoint[] {
  return monaco.languages.getLanguages().filter((language) => !registeredIds.has(language.id));
}

function regex(value: unknown): RegExp | undefined {
  return typeof value === "string" ? new RegExp(value) : undefined;
}

function pair(value: unknown): { open: string; close: string; notIn?: string[] } {
  if (Array.isArray(value)) return { open: String(value[0]), close: String(value[1]) };
  const record = value as { open: string; close: string; notIn?: string[] };
  return { open: record.open, close: record.close, ...(record.notIn ? { notIn: [...record.notIn] } : {}) };
}

/** Converts the host-validated configuration JSON into Monaco's shape (regex strings → RegExp). */
function toLanguageConfiguration(config: Record<string, unknown>): languages.LanguageConfiguration {
  const result: languages.LanguageConfiguration = {};
  const comments = config.comments as { lineComment?: string; blockComment?: [string, string] } | undefined;
  if (comments) result.comments = { ...comments };
  if (Array.isArray(config.brackets)) result.brackets = config.brackets.map((item) => [item[0], item[1]]);
  if (Array.isArray(config.colorizedBracketPairs)) {
    result.colorizedBracketPairs = config.colorizedBracketPairs.map((item) => [item[0], item[1]]);
  }
  if (Array.isArray(config.autoClosingPairs)) result.autoClosingPairs = config.autoClosingPairs.map(pair);
  if (Array.isArray(config.surroundingPairs)) {
    result.surroundingPairs = config.surroundingPairs.map((item) => {
      const { open, close } = pair(item);
      return { open, close };
    });
  }
  if (typeof config.autoCloseBefore === "string") result.autoCloseBefore = config.autoCloseBefore;
  const wordPattern = regex(config.wordPattern);
  if (wordPattern) result.wordPattern = wordPattern;
  const folding = config.folding as { markers?: { start: string; end: string }; offSide?: boolean } | undefined;
  if (folding) {
    result.folding = {
      ...(folding.offSide !== undefined ? { offSide: folding.offSide } : {}),
      ...(folding.markers ? { markers: { start: new RegExp(folding.markers.start), end: new RegExp(folding.markers.end) } } : {}),
    };
  }
  const indentation = config.indentationRules as Record<string, string> | undefined;
  if (indentation) {
    result.indentationRules = {
      increaseIndentPattern: new RegExp(indentation.increaseIndentPattern),
      decreaseIndentPattern: new RegExp(indentation.decreaseIndentPattern),
      ...(indentation.indentNextLinePattern ? { indentNextLinePattern: new RegExp(indentation.indentNextLinePattern) } : {}),
      ...(indentation.unIndentedLinePattern ? { unIndentedLinePattern: new RegExp(indentation.unIndentedLinePattern) } : {}),
    };
  }
  return result;
}

function issue(language: PluginLanguage, code: LanguagePluginIssue["code"], message: string): LanguagePluginIssue {
  return { pluginId: language.pluginId, entry: language.entry, language: language.id, code, message, stage: "frontend" };
}

/** Probes, then installs the tokenizer and configuration of one language unless superseded. */
function install(monaco: MonacoApi, language: PluginLanguage, state: AppliedLanguage) {
  if (!language.grammar && !language.configuration) return;
  void probeRegexes(language.fingerprint, language.probe).then((result) => {
    if (active !== monaco || applied.get(language.id) !== state) return;
    if (result !== "ok") {
      state.issues.push(issue(language, "probeTimeout", result === "timeout"
        ? "grammar regex probe timed out (possible catastrophic backtracking); the language is registered without highlighting"
        : "grammar regex probe is unavailable; the language is registered without highlighting"));
      notify();
      return;
    }
    if (language.grammar) {
      try {
        // Monarch's compiler mutates the definition it compiles.
        const grammar = structuredClone(language.grammar) as unknown as languages.IMonarchLanguage;
        state.disposers.push(monaco.languages.setMonarchTokensProvider(language.id, grammar));
      } catch (error) {
        state.issues.push(issue(language, "grammarRejected", `Monarch rejected the grammar: ${error instanceof Error ? error.message : String(error)}`));
      }
    }
    if (language.configuration) {
      try {
        state.disposers.push(monaco.languages.setLanguageConfiguration(language.id, toLanguageConfiguration(language.configuration)));
      } catch (error) {
        state.issues.push(issue(language, "configurationRejected", `Monaco rejected the configuration: ${error instanceof Error ? error.message : String(error)}`));
      }
    }
    notify();
  });
}

function disposeApplied(id: string) {
  const state = applied.get(id);
  if (!state) return;
  applied.delete(id);
  for (const disposer of state.disposers) {
    try {
      disposer.dispose();
    } catch {
      // A failed dispose must not keep other languages from reconciling.
    }
  }
}

/** Registers/disposes so Monaco matches the desired plugin languages. */
function reconcile() {
  const monaco = active;
  if (!monaco) {
    associations = [];
    stageIssues = [];
    return;
  }
  const builtIns = builtInLanguages(monaco);
  const builtInIds = new Set(builtIns.map((language) => language.id));
  const issues: LanguagePluginIssue[] = [];
  const owned = new Map<string, PluginLanguage>();
  const eligible: PluginLanguage[] = [];
  for (const language of desired) {
    if (!language.associationOnly && builtInIds.has(language.id)) {
      issues.push(issue(language, "registerFailed", `language id "${language.id}" is already registered by MinCode`));
      continue;
    }
    if (!language.associationOnly) owned.set(language.id, language);
    eligible.push(language);
  }
  for (const [id, state] of [...applied]) {
    if (owned.get(id)?.fingerprint !== state.fingerprint) disposeApplied(id);
  }
  for (const [id, language] of owned) {
    if (!registeredIds.has(id)) {
      try {
        monaco.languages.register({ id, aliases: [...language.aliases] });
        registeredIds.add(id);
      } catch (error) {
        issues.push(issue(language, "registerFailed", `Monaco rejected language "${id}": ${error instanceof Error ? error.message : String(error)}`));
        continue;
      }
    }
    if (applied.has(id)) continue;
    const state: AppliedLanguage = { fingerprint: language.fingerprint, disposers: [], issues: [] };
    applied.set(id, state);
    install(monaco, language, state);
  }
  const knownIds = new Set([...builtInIds, ...[...owned.keys()].filter((id) => registeredIds.has(id))]);
  const resolved = resolvePluginLanguageAssociations(eligible, builtIns, knownIds);
  associations = resolved.definitions;
  stageIssues = [...issues, ...resolved.issues];
}

function detach() {
  for (const id of [...applied.keys()]) disposeApplied(id);
  const hadState = associations.length > 0;
  associations = [];
  stageIssues = [];
  active = undefined;
  if (hadState) notify();
}

/**
 * Attaches the registry to a loaded Monaco (the contribution hook calls
 * this; tests pass a stub). Returns a disposer that removes every tokenizer
 * and configuration and every association.
 */
export function attachMonacoLanguages(monaco: MonacoApi): () => void {
  detach();
  active = monaco;
  schedule();
  return detach;
}

/** Replaces every plugin language (an empty list removes them all). */
export function setMonacoLanguages(languages: readonly PluginLanguage[]): void {
  desired = languages;
  if (languages.length > 0 && !unregisterHook) {
    unregisterHook = registerMonacoContributionHook((monaco) => attachMonacoLanguages(monaco));
  }
  if (active) schedule();
}

/**
 * Detects the Monaco language for `path`: built-in detection first (with the
 * first line). A plugin compound suffix pattern more specific than the
 * built-in extension (`*.blade.php` over `.php`) overrides a built-in result;
 * other plugin associations apply only when built-in detection yields
 * plaintext and the file is not explicitly a plaintext file.
 */
export function detectMonacoEditorLanguage(monaco: MonacoApi, path: string, value: string): string {
  const firstLine = value.split(/\r?\n/, 1)[0] ?? "";
  const builtIns = builtInLanguages(monaco);
  const detected = detectProjectEditorLanguage(path, builtIns, firstLine);
  if (associations.length === 0) return detected;
  if (detected !== "plaintext") return detectCompoundSuffixLanguage(path, associations, builtIns) ?? detected;
  const plaintext = builtIns.find((language) => language.id === "plaintext");
  const explicitMarker = "\u0000plaintext";
  if (plaintext && detectProjectEditorLanguage(path, [{ ...plaintext, id: explicitMarker }]) === explicitMarker) return detected;
  return detectProjectEditorLanguage(path, associations);
}

/** Monaco-stage issues (conflicts, unknown ids, probe timeouts, Monaco rejections). */
export function monacoLanguageIssues(): readonly LanguagePluginIssue[] {
  return [...stageIssues, ...[...applied.values()].flatMap((state) => state.issues)];
}

/** Plugin association definitions currently in effect (diagnostics and tests). */
export function pluginLanguageAssociations(): readonly ProjectEditorLanguageDefinition[] {
  return associations;
}

/** Plugin language ids with an applied tokenizer/configuration state (diagnostics and tests). */
export function monacoPluginLanguageIds(): string[] {
  return [...applied.keys()].sort();
}
