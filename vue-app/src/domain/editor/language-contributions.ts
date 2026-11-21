/**
 * Frontend stage of the `languages` contribution point. The Go host already
 * validated each entry and inlined its Monarch grammar and language
 * configuration (backend/plugin/contrib_languages.go) and sends
 * `{id, aliases, extensions, filenames, filenamePatterns, grammarPath?,
 * grammar?, configurationPath?, configuration?}`. This module re-checks that
 * shape (entries are untrusted), compiles every grammar and configuration
 * regex and runs the static ReDoS heuristics, and resolves plugin-vs-plugin
 * conflicts deterministically: plugins are processed in pluginId order and the
 * first plugin to claim a new language id, extension or filename keeps it.
 *
 * Built-in precedence (built-in ids and built-in extension/filename
 * associations can never be overridden) needs Monaco's language list, so it is
 * applied by `resolvePluginLanguageAssociations` in the Monaco stage
 * (services/monaco-languages.ts). Pure: no Monaco, Vue or IPC.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import {
  collectConfigurationRegexes,
  collectMonarchRegexes,
  fnv1a,
  regexListIssue,
  type MonarchGrammar,
  type MonarchRegex,
} from "@/domain/editor/monarch";
import {
  detectProjectEditorLanguage,
  type ProjectEditorLanguageDefinition,
} from "@/domain/projects/editor-language";

export const LANGUAGE_CONTRIBUTION_POINT = "languages";
export const LANGUAGE_ID_PATTERN = /^[a-z][a-z0-9._-]{0,31}$/;
const EXTENSION_PATTERN = /^\.[A-Za-z0-9][A-Za-z0-9._+-]{0,31}$/;
const MAX_ALIASES = 8;
const MAX_ASSOCIATIONS = 32;
const MAX_PATTERNS = 16;

export interface PluginLanguage {
  id: string;
  aliases: string[];
  extensions: string[];
  filenames: string[];
  filenamePatterns: string[];
  /** Absent for association-only entries (built-in ids) and rejected grammars. */
  grammar?: MonarchGrammar;
  configuration?: Record<string, unknown>;
  /** Regexes the worker probe must exercise before the grammar tokenizes. */
  probe: MonarchRegex[];
  /** Hash of grammar + configuration (probe cache key and reconcile diff). */
  fingerprint: string;
  /** True when the entry only adds associations to a language it does not own. */
  associationOnly: boolean;
  pluginId: string;
  pluginName: string;
  /** Index into the plugin's contributes.languages. */
  entry: number;
}

export type LanguageIssueCode =
  | "malformed"
  | "duplicateId"
  | "grammarRejected"
  | "configurationRejected"
  | "associationConflict"
  | "probeTimeout"
  | "registerFailed";

export interface LanguagePluginIssue {
  pluginId: string;
  /** Index into contributes.languages, or -1 for issues across entries. */
  entry: number;
  language?: string;
  code?: LanguageIssueCode;
  message: string;
  /** `host`: rejected by the Go manifest validator; `frontend`: rejected here or by the Monaco stage. */
  stage: "host" | "frontend";
}

export interface LanguageRegistry {
  /** Accepted entries in pluginId, then entry order. */
  languages: PluginLanguage[];
  issues: LanguagePluginIssue[];
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function stringList(value: unknown, max: number, valid: (item: string) => boolean): string[] | undefined {
  if (value === undefined) return [];
  if (!Array.isArray(value) || value.length > max) return undefined;
  return value.every((item) => typeof item === "string" && valid(item)) ? [...(value as string[])] : undefined;
}

const validAlias = (alias: string) =>
  alias.trim().length > 0 && [...alias].length <= 64 && !/[\p{Cc}\u2028\u2029\u202a-\u202e\u2066-\u2069]/u.test(alias);
const validFilename = (name: string) => name.length > 0 && name.length <= 64 && !/[/\\\p{Cc}]/u.test(name) && name !== "." && name !== "..";
const validPattern = (pattern: string) => pattern.length > 0 && pattern.length <= 128 && /^[A-Za-z0-9._\-/*?+@]+$/.test(pattern);

/**
 * Validates every contributed language and resolves conflicts between
 * plugins. A malformed entry is skipped; a grammar or configuration that
 * fails a regex check is dropped while the language and its associations stay
 * (the file then opens as an untokenized language). Every skip is reported.
 */
export function buildLanguageRegistry(contributions: readonly PointContribution[]): LanguageRegistry {
  const languages: PluginLanguage[] = [];
  const issues: LanguagePluginIssue[] = [];
  const ownedIds = new Map<string, string>();
  const sorted = [...contributions].sort((a, b) => (a.pluginId < b.pluginId ? -1 : a.pluginId > b.pluginId ? 1 : 0));
  for (const contribution of sorted) {
    for (const issue of contribution.point.issues) {
      issues.push({
        pluginId: contribution.pluginId,
        stage: "host",
        entry: issue.entry,
        message: issue.message,
        ...(issue.id ? { language: issue.id } : {}),
      });
    }
    contribution.point.entries.forEach((value, entry) => {
      const record = objectValue(value);
      const id = typeof record?.id === "string" ? record.id : "";
      const report = (code: LanguageIssueCode, message: string) =>
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, code, message, ...(id ? { language: id } : {}) });
      const aliases = stringList(record?.aliases, MAX_ALIASES, validAlias);
      const extensions = stringList(record?.extensions, MAX_ASSOCIATIONS, (item) => EXTENSION_PATTERN.test(item));
      const filenames = stringList(record?.filenames, MAX_ASSOCIATIONS, validFilename);
      const filenamePatterns = stringList(record?.filenamePatterns, MAX_PATTERNS, validPattern);
      if (!record || !LANGUAGE_ID_PATTERN.test(id) || !aliases || !extensions || !filenames || !filenamePatterns) {
        report("malformed", "malformed language entry");
        return;
      }
      const associationOnly = aliases.length === 0;
      if (associationOnly && (record.grammar !== undefined || record.configuration !== undefined)) {
        report("malformed", "an association-only entry cannot carry a grammar or configuration");
        return;
      }
      if (!associationOnly) {
        const owner = ownedIds.get(id);
        if (owner !== undefined) {
          report("duplicateId", `language id "${id}" is already contributed by ${owner}`);
          return;
        }
        ownedIds.set(id, contribution.pluginId);
      }
      let grammar: MonarchGrammar | undefined;
      let configuration: Record<string, unknown> | undefined;
      const probe: MonarchRegex[] = [];
      const grammarRecord = objectValue(record.grammar);
      if (record.grammar !== undefined) {
        try {
          if (!grammarRecord || !objectValue(grammarRecord.tokenizer)) throw new Error("grammar must define a tokenizer object");
          const regexes = collectMonarchRegexes(grammarRecord as MonarchGrammar);
          const problem = regexListIssue(regexes);
          if (problem) throw new Error(problem);
          grammar = grammarRecord as MonarchGrammar;
          probe.push(...regexes);
        } catch (error) {
          report("grammarRejected", `grammar rejected: ${error instanceof Error ? error.message : String(error)}`);
        }
      }
      const configurationRecord = objectValue(record.configuration);
      if (record.configuration !== undefined) {
        const regexes = configurationRecord ? collectConfigurationRegexes(configurationRecord) : [];
        const problem = configurationRecord ? regexListIssue(regexes) : "configuration must be an object";
        if (problem) report("configurationRejected", `configuration rejected: ${problem}`);
        else {
          configuration = configurationRecord;
          probe.push(...regexes);
        }
      }
      languages.push({
        id,
        aliases,
        extensions,
        filenames,
        filenamePatterns,
        ...(grammar ? { grammar } : {}),
        ...(configuration ? { configuration } : {}),
        probe,
        fingerprint: fnv1a(JSON.stringify([grammar ?? null, configuration ?? null])),
        associationOnly,
        pluginId: contribution.pluginId,
        pluginName: contribution.pluginName || contribution.pluginId,
        entry,
      });
    });
  }
  return { languages, issues };
}

/** Install-preview summary of `contributes.languages` (manifest data; grammar files are not read). */
export function languagePreviewSummary(contributes: unknown): { languages: string[]; associations: string[] } {
  const entries = objectValue(contributes)?.languages;
  const languages: string[] = [];
  const associations: string[] = [];
  if (!Array.isArray(entries)) return { languages, associations };
  for (const raw of entries.slice(0, 16)) {
    const entry = objectValue(raw);
    const id = typeof entry?.id === "string" ? entry.id : "";
    if (!LANGUAGE_ID_PATTERN.test(id)) continue;
    const targets = ["extensions", "filenames", "filenamePatterns"]
      .flatMap((key) => (Array.isArray(entry?.[key]) ? (entry[key] as unknown[]) : []))
      .filter((item): item is string => typeof item === "string")
      .slice(0, 6);
    const label = targets.length ? `${id} (${targets.join(", ")})` : id;
    if (Array.isArray(entry?.aliases) && entry.aliases.length > 0) languages.push(label);
    else associations.push(label);
  }
  return { languages, associations };
}

/**
 * Drops plugin associations that built-in detection already resolves (a
 * built-in extension or filename always wins) and associations an earlier
 * plugin (by pluginId) already claimed. `builtIns` are Monaco's own language
 * definitions. `ownIds` limits association targets to ids Monaco knows.
 */
export function resolvePluginLanguageAssociations(
  languages: readonly PluginLanguage[],
  builtIns: readonly ProjectEditorLanguageDefinition[],
  knownIds: ReadonlySet<string>,
): { definitions: ProjectEditorLanguageDefinition[]; issues: LanguagePluginIssue[] } {
  const definitions: ProjectEditorLanguageDefinition[] = [];
  const issues: LanguagePluginIssue[] = [];
  const claimed = new Map<string, string>();
  const builtInExtensions = new Set(builtIns.flatMap((language) => language.extensions ?? []).map((item) => item.toLowerCase()));
  const builtInFilenames = new Set(builtIns.flatMap((language) => language.filenames ?? []).map((item) => item.toLowerCase()));
  for (const language of languages) {
    if (!knownIds.has(language.id)) {
      issues.push({ pluginId: language.pluginId, entry: language.entry, language: language.id, code: "registerFailed", stage: "frontend", message: `unknown Monaco language "${language.id}"` });
      continue;
    }
    const keep = (kind: "extension" | "filename" | "pattern", value: string) => {
      const key = `${kind}:${value.toLowerCase()}`;
      const builtIn =
        kind === "extension"
          ? builtInExtensions.has(value.toLowerCase()) || detectProjectEditorLanguage(`file${value}`, builtIns) !== "plaintext"
          : kind === "filename"
            ? builtInFilenames.has(value.toLowerCase()) || detectProjectEditorLanguage(value, builtIns) !== "plaintext"
            : false;
      const owner = claimed.get(key);
      if (builtIn || (owner !== undefined && owner !== language.pluginId)) {
        issues.push({
          pluginId: language.pluginId,
          entry: language.entry,
          language: language.id,
          code: "associationConflict",
          stage: "frontend",
          message: builtIn
            ? `${kind} "${value}" is already associated with a built-in language`
            : `${kind} "${value}" is already associated by ${owner}`,
        });
        return false;
      }
      claimed.set(key, language.pluginId);
      return true;
    };
    const extensions = language.extensions.filter((value) => keep("extension", value));
    const filenames = language.filenames.filter((value) => keep("filename", value));
    const filenamePatterns = language.filenamePatterns.filter((value) => keep("pattern", value));
    if (extensions.length || filenames.length || filenamePatterns.length) {
      definitions.push({ id: language.id, extensions, filenames, filenamePatterns });
    }
  }
  // detectProjectEditorLanguage lets later definitions win ties; the first plugin by id must win.
  definitions.reverse();
  return { definitions, issues };
}
