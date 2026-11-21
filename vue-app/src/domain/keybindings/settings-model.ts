/**
 * Pure view model of the keybinding settings page: table rows, search and
 * filters, recorder chord accumulation, live conflict analysis of a candidate
 * key, override edits for the row actions, and the import preview / merge.
 * Everything here is DOM- and store-free so it is covered by node tests; the
 * settings components only render the results and write them to the store.
 */
import { parseWhenExpression } from "@/domain/commands/when";
import type { KeybindingCategory, KeybindingCommand, KeybindingHandlerKind, KeybindingScope } from "@/domain/keybindings/catalog";
import type { KeybindingImportIssue, KeybindingImportResult } from "@/domain/keybindings/import-export";
import { exportKeybindings } from "@/domain/keybindings/import-export";
import {
  chordId,
  formatKeySequence,
  parseKeySequence,
  sequenceId,
  type KeyChord,
  type KeySequence,
  type KeyboardPlatform,
} from "@/domain/keybindings/keys";
import { MAX_KEYBINDING_OVERRIDES, overridesToRules, type KeybindingOverride } from "@/domain/keybindings/persistence";
import { MINCODE_PRESET_ID, presetRulesForPlatform, type KeybindingRule } from "@/domain/keybindings/preset";
import { reservedKeyReason, type ReservedKeyReason } from "@/domain/keybindings/reserved";
import {
  findKeybindingConflicts,
  formatWhenClauses,
  keybindingsConflict,
  resolveKeybindings,
  type KeybindingLayer,
  type KeybindingResolution,
  type ResolvedKeybinding,
} from "@/domain/keybindings/resolver";
import type { MessageKey } from "@/i18n/locales/zh";

/** The OS-wide activation shortcut registered by App.vue (`CmdOrCtrl+Shift+Space`) when enabled. */
export const GLOBAL_ACTIVATION_KEY = "mod+shift+space";
/** Time window in which a second recorded chord extends the first (`⌘K ⌘S`). */
export const RECORDER_CHORD_TIMEOUT_MS = 1500;

export type KeybindingRowScope = KeybindingScope;
export type KeybindingRowSource = "default" | "preset" | "user" | "removed" | "none";
export type KeybindingRowFilter = "all" | "modified" | "conflicts" | `scope:${KeybindingRowScope}`;

/** A command listed in the shortcut settings table. */
export interface KeybindingRowCommand {
  id: string;
  titleKey?: MessageKey;
  title?: string;
  category: KeybindingCategory;
  scope: KeybindingRowScope;
  handler: KeybindingHandlerKind;
  available: boolean;
}

export interface KeybindingRowBinding {
  /** Canonical key text (`meta+k meta+s`); parses back with parseKeySequence. */
  key: string;
  label: string;
  when?: string;
  layer: KeybindingLayer;
  reserved?: ReservedKeyReason;
  conflict: boolean;
  binding: ResolvedKeybinding;
}

export interface KeybindingRow extends KeybindingRowCommand {
  /** Effective bindings, highest priority first. */
  bindings: KeybindingRowBinding[];
  source: KeybindingRowSource;
  /** The user has overrides for this command. */
  modified: boolean;
  conflictCount: number;
}

export function catalogRowCommand(command: KeybindingCommand): KeybindingRowCommand {
  return {
    id: command.id,
    titleKey: command.titleKey,
    category: command.category,
    scope: command.scope,
    handler: command.handler,
    available: command.available,
  };
}

function rowSource(
  bindings: readonly ResolvedKeybinding[],
  overrides: readonly KeybindingOverride[],
  presetId: string,
): KeybindingRowSource {
  if (bindings.some((binding) => binding.layer === "user")) return "user";
  if (overrides.some((override) => override.remove)) return "removed";
  if (!bindings.length) return "none";
  if (presetId !== MINCODE_PRESET_ID && bindings.some((binding) => binding.layer === "preset")) return "preset";
  return "default";
}

export interface BuildKeybindingRowsInput {
  commands: readonly KeybindingRowCommand[];
  resolution: KeybindingResolution;
  overrides: readonly KeybindingOverride[];
  /** Effective preset id (the fallback when the stored one is unavailable). */
  presetId: string;
  platform: KeyboardPlatform;
}

export function buildKeybindingRows(input: BuildKeybindingRowsInput): KeybindingRow[] {
  const conflicted = new Set<ResolvedKeybinding>();
  for (const conflict of findKeybindingConflicts(input.resolution.bindings)) {
    conflicted.add(conflict.first);
    conflicted.add(conflict.second);
  }
  const seen = new Set<string>();
  const commands: KeybindingRowCommand[] = [];
  for (const command of input.commands) {
    if (seen.has(command.id)) continue;
    seen.add(command.id);
    commands.push(command);
  }
  return commands.map((command) => {
    const resolved = [...(input.resolution.byCommand.get(command.id) ?? [])].reverse();
    const overrides = input.overrides.filter((override) => override.command === command.id);
    const bindings = resolved.map<KeybindingRowBinding>((binding) => {
      const row: KeybindingRowBinding = {
        key: binding.sequenceKey,
        label: formatKeySequence(binding.sequence, input.platform),
        layer: binding.layer,
        conflict: conflicted.has(binding),
        binding,
      };
      if (binding.when) row.when = binding.when;
      if (binding.reserved) row.reserved = binding.reserved;
      return row;
    });
    return {
      ...command,
      bindings,
      source: rowSource(resolved, overrides, input.presetId),
      modified: overrides.length > 0,
      conflictCount: bindings.filter((binding) => binding.conflict).length,
    };
  });
}

export interface KeybindingRowQuery {
  text: string;
  /** Canonical sequence id recorded by the "search by key" toggle. */
  keySequence?: string;
  filter: KeybindingRowFilter;
}

function sequenceMatches(bindingKey: string, query: string): boolean {
  return bindingKey === query || bindingKey.startsWith(`${query} `);
}

/** Filters rows by text (title, id, key label or key text such as "cmd+k"), recorded key and filter. */
export function filterKeybindingRows(
  rows: readonly KeybindingRow[],
  query: KeybindingRowQuery,
  titleOf: (row: KeybindingRow) => string,
  platform: KeyboardPlatform,
): KeybindingRow[] {
  const text = query.text.trim().toLowerCase();
  const parsed = text ? parseKeySequence(text, platform) : null;
  const parsedKey = parsed ? sequenceId(parsed) : "";
  const tokens = text.split(/\s+/).filter(Boolean);
  return rows.filter((row) => {
    if (query.filter === "modified" && !row.modified) return false;
    if (query.filter === "conflicts" && row.conflictCount === 0) return false;
    if (query.filter.startsWith("scope:") && row.scope !== query.filter.slice("scope:".length)) return false;
    if (query.keySequence && !row.bindings.some((binding) => sequenceMatches(binding.key, query.keySequence!))) return false;
    if (!text) return true;
    if (parsedKey && row.bindings.some((binding) => sequenceMatches(binding.key, parsedKey))) return true;
    const haystack = [titleOf(row), row.id, ...row.bindings.flatMap((binding) => [binding.label, binding.key])]
      .join("\n")
      .toLowerCase();
    return tokens.every((token) => haystack.includes(token));
  });
}

// ---------------------------------------------------------------------------
// Recorder
// ---------------------------------------------------------------------------

export interface RecorderState {
  chords: KeyChord[];
  /** Timestamp of the last recorded chord. */
  at: number;
}

export const EMPTY_RECORDER_STATE: RecorderState = Object.freeze({ chords: [], at: 0 }) as RecorderState;

/** Adds a chord: a second chord within the timeout extends the first, otherwise recording restarts. */
export function recordChord(state: RecorderState, chord: KeyChord, now: number): RecorderState {
  if (state.chords.length === 1 && now - state.at <= RECORDER_CHORD_TIMEOUT_MS) {
    return { chords: [state.chords[0]!, chord], at: now };
  }
  return { chords: [chord], at: now };
}

export function recorderSequence(state: RecorderState): KeySequence | null {
  return state.chords.length ? { kind: "chords", chords: state.chords } : null;
}

export interface CandidateAnalysis {
  /** The candidate as the resolver would see it (implicit focus clauses included). */
  binding: ResolvedKeybinding | null;
  /** Error for an invalid key or when expression. */
  error?: "key" | "when";
  /** Other commands' bindings whose sequence collides and whose when can overlap. */
  conflicts: ResolvedKeybinding[];
  reserved?: ReservedKeyReason;
  /** Collides with the app's own OS-wide activation shortcut (when that is enabled). */
  globalShortcut: boolean;
}

export interface CandidateInput {
  resolution: KeybindingResolution;
  platform: KeyboardPlatform;
  command: string;
  key: string;
  when?: string;
  globalShortcutEnabled?: boolean;
}

export function whenExpressionValid(when: string | undefined): boolean {
  if (!when?.trim()) return true;
  try {
    parseWhenExpression(when);
    return true;
  } catch {
    return false;
  }
}

/** Live conflict / reserved-key analysis of a recorded key for one command. */
export function analyzeCandidate(input: CandidateInput): CandidateAnalysis {
  const empty: CandidateAnalysis = { binding: null, conflicts: [], globalShortcut: false };
  const sequence = parseKeySequence(input.key, input.platform);
  if (!sequence) return { ...empty, error: "key" };
  if (!whenExpressionValid(input.when)) return { ...empty, error: "when" };
  const rule: KeybindingRule = { command: input.command, key: input.key };
  if (input.when?.trim()) rule.when = input.when.trim();
  const resolved = resolveKeybindings({ platform: input.platform, presets: [], user: [rule] }).bindings[0];
  if (!resolved) return { ...empty, error: "key" };
  // Saved as a user override it outranks every existing binding.
  const candidate: ResolvedKeybinding = { ...resolved, order: Number.MAX_SAFE_INTEGER };
  const conflicts = input.resolution.bindings.filter(
    (binding) => binding.command !== input.command && keybindingsConflict(candidate, binding) !== null,
  );
  const result: CandidateAnalysis = { binding: candidate, conflicts, globalShortcut: false };
  const reserved = reservedKeyReason(sequence, input.platform);
  if (reserved) result.reserved = reserved;
  const activation = parseKeySequence(GLOBAL_ACTIVATION_KEY, input.platform);
  if (input.globalShortcutEnabled && activation?.kind === "chords" && sequence.kind === "chords") {
    result.globalShortcut = chordId(sequence.chords[0]!) === chordId(activation.chords[0]!);
  }
  return result;
}

// ---------------------------------------------------------------------------
// Override edits (row actions)
// ---------------------------------------------------------------------------

function normalizedWhen(when: string | undefined): string {
  if (!when?.trim()) return "";
  try {
    return formatWhenClauses(parseWhenExpression(when));
  } catch {
    return when.trim();
  }
}

function appliesTo(override: KeybindingOverride, platform: KeyboardPlatform): boolean {
  if (override.platform === undefined) return true;
  return Array.isArray(override.platform) ? override.platform.includes(platform) : override.platform === platform;
}

/** Index of the last non-removal override that produced `target` (a user-layer binding). */
export function userOverrideIndex(
  overrides: readonly KeybindingOverride[],
  target: Pick<ResolvedKeybinding, "command" | "sequenceKey" | "when">,
  platform: KeyboardPlatform,
): number {
  for (let index = overrides.length - 1; index >= 0; index--) {
    const override = overrides[index]!;
    if (override.remove || override.command !== target.command || !override.key || !appliesTo(override, platform)) continue;
    const sequence = parseKeySequence(override.key, platform);
    if (!sequence || sequenceId(sequence) !== target.sequenceKey) continue;
    if (normalizedWhen(override.when) !== normalizedWhen(target.when)) continue;
    return index;
  }
  return -1;
}

function removalOf(target: ResolvedKeybinding, platform: KeyboardPlatform): KeybindingOverride {
  const removal: KeybindingOverride = { command: target.command, key: target.sequenceKey, remove: true, platform };
  if (target.when) removal.when = target.when;
  return removal;
}

function addition(command: string, key: string, when: string | undefined, platform: KeyboardPlatform): KeybindingOverride {
  const override: KeybindingOverride = { command, key, platform };
  if (when?.trim()) override.when = when.trim();
  return override;
}

export interface OverrideChange {
  command: string;
  /** The binding being changed; omitted to add a new (alternate) binding. */
  target?: ResolvedKeybinding;
  key: string;
  when?: string;
  platform: KeyboardPlatform;
}

/**
 * Recorded keys are stored for the platform they were recorded on (like VS
 * Code's per-OS keybindings.json): physical keys differ between platforms.
 */
export function overridesAfterChange(overrides: readonly KeybindingOverride[], change: OverrideChange): KeybindingOverride[] {
  const next = [...overrides];
  const added = addition(change.command, change.key, change.when, change.platform);
  if (change.target?.layer === "user") {
    const index = userOverrideIndex(next, change.target, change.platform);
    if (index >= 0) {
      next.splice(index, 1, added);
      return next;
    }
  }
  if (change.target) next.push(removalOf(change.target, change.platform));
  next.push(added);
  return next;
}

/** Removes one binding (or, without `target`, every binding of the command on this platform). */
export function overridesAfterRemove(
  overrides: readonly KeybindingOverride[],
  command: string,
  platform: KeyboardPlatform,
  target?: ResolvedKeybinding,
): KeybindingOverride[] {
  if (target) {
    if (target.layer === "user") {
      const index = userOverrideIndex(overrides, target, platform);
      if (index >= 0) return overrides.filter((_, current) => current !== index);
    }
    return [...overrides, removalOf(target, platform)];
  }
  return [
    ...overrides.filter((override) => override.command !== command || override.remove || !appliesTo(override, platform)),
    { command, remove: true, platform },
  ];
}

export function overridesAfterReset(overrides: readonly KeybindingOverride[], command: string): KeybindingOverride[] {
  return overrides.filter((override) => override.command !== command);
}

// ---------------------------------------------------------------------------
// Import / export
// ---------------------------------------------------------------------------

export interface KeybindingImportPreviewEntry {
  command: string;
  removal: boolean;
  key?: string;
  /** Formatted key label for the current platform ("" for remove-all rules). */
  label: string;
  when?: string;
}

export interface KeybindingImportPreview {
  mapped: KeybindingImportPreviewEntry[];
  unsupported: KeybindingImportIssue[];
}

export function buildImportPreview(result: KeybindingImportResult, platform: KeyboardPlatform): KeybindingImportPreview {
  const mapped = result.rules.map<KeybindingImportPreviewEntry>((rule) => {
    const removal = rule.command.startsWith("-");
    const sequence = rule.key ? parseKeySequence(rule.key, platform) : null;
    const entry: KeybindingImportPreviewEntry = {
      command: removal ? rule.command.slice(1) : rule.command,
      removal,
      label: sequence ? formatKeySequence(sequence, platform) : "",
    };
    if (rule.key) entry.key = rule.key;
    if (rule.when) entry.when = rule.when;
    return entry;
  });
  return { mapped, unsupported: result.issues };
}

export function importedOverrides(preview: KeybindingImportPreview, platform: KeyboardPlatform): KeybindingOverride[] {
  return preview.mapped.map((entry) => {
    const override: KeybindingOverride = { command: entry.command, platform };
    if (entry.key) override.key = entry.key;
    if (entry.when) override.when = entry.when;
    if (entry.removal) override.remove = true;
    return override;
  });
}

/** Merge appends imported overrides that are not already present; replace uses only the imported ones. */
export function applyImportedOverrides(
  existing: readonly KeybindingOverride[],
  imported: readonly KeybindingOverride[],
  mode: "merge" | "replace",
): KeybindingOverride[] {
  if (mode === "replace") return imported.slice(0, MAX_KEYBINDING_OVERRIDES);
  const keys = new Set(existing.map((override) => JSON.stringify(override)));
  const merged = [...existing];
  for (const override of imported) {
    const id = JSON.stringify(override);
    if (keys.has(id)) continue;
    keys.add(id);
    merged.push(override);
  }
  return merged.slice(0, MAX_KEYBINDING_OVERRIDES);
}

/** The user's overrides for this platform as a VS Code-shaped keybindings.json document. */
export function exportUserKeybindings(overrides: readonly KeybindingOverride[], platform: KeyboardPlatform): string {
  return exportKeybindings(presetRulesForPlatform(overridesToRules(overrides), platform));
}
