/**
 * Pure conversion of resolved editor keybindings into Monaco keybinding rules
 * (`monaco.editor.addKeybindingRules`). Monaco enums are injected so this
 * module never imports Monaco. Phase 3's services/monaco-keymap.ts applies the
 * result and also mirrors app-level bindings; this module covers:
 *
 *  (a) rules for preset/user bindings of Monaco commands, when translated
 *      (editor.focus → editorTextFocus, editor.kind == "diff" → isInDiffEditor,
 *      editor.kind == "x" → mincodeEditorKind == 'x'); untranslatable rules
 *      are dropped and reported;
 *  (b) for every command whose bindings differ from Monaco's defaults, removal
 *      rules for all of its defaults, then its resolved bindings re-added;
 *  (c) removal of colliding Monaco defaults of other commands (catalog or
 *      not, e.g. deleteAllLeft, redo) when an added rule applies to the whole
 *      editor (clauses ⊆ {editor.focus}).
 */
import type { WhenClause } from "@/domain/commands/when";
import { getKeybindingCommand } from "@/domain/keybindings/catalog";
import { chordId, parseKeySequence, sequenceId, type KeyChord, type KeySequence, type KeyboardPlatform } from "@/domain/keybindings/keys";
import { monacoDefaultBindings, type MonacoDefaultBinding } from "@/domain/keybindings/monaco-defaults";
import type { KeybindingResolution, ResolvedKeybinding } from "@/domain/keybindings/resolver";

export interface MonacoKeyApi {
  KeyMod: { CtrlCmd: number; Shift: number; Alt: number; WinCtrl: number };
  KeyCode: Record<string, number>;
  chord(first: number, second: number): number;
}

export interface MonacoKeybindingRule {
  keybinding: number;
  /** Monaco command id; `-id` removes a default binding. */
  command: string;
  when?: string;
  /** Resolver order of the binding an addition came from (higher wins); absent on removals. */
  order?: number;
}

export interface MonacoRuleSkip {
  binding: ResolvedKeybinding;
  reason: "when" | "key" | "editor-mode";
  detail: string;
}

export interface MonacoRulesInput {
  platform: KeyboardPlatform;
  resolution: KeybindingResolution;
  api: MonacoKeyApi;
  /** Sequences owned by the active editor-mode provider (e.g. Vim); rules on them are skipped. */
  editorModeOwns?: (sequence: KeySequence) => boolean;
}

export interface MonacoRulesResult {
  rules: MonacoKeybindingRule[];
  skipped: MonacoRuleSkip[];
  /** Monaco commands whose defaults were replaced. */
  changedCommands: string[];
}

const CODE_TO_MONACO: Record<string, string> = {
  ArrowUp: "UpArrow",
  ArrowDown: "DownArrow",
  ArrowLeft: "LeftArrow",
  ArrowRight: "RightArrow",
};

function monacoKeyCode(code: string, api: MonacoKeyApi): number | null {
  const name = CODE_TO_MONACO[code] ?? code;
  const value = api.KeyCode[name];
  return typeof value === "number" ? value : null;
}

export function monacoChord(chord: KeyChord, platform: KeyboardPlatform, api: MonacoKeyApi): number | null {
  const code = monacoKeyCode(chord.code, api);
  if (code === null) return null;
  const primary = platform === "mac" ? chord.meta : chord.ctrl;
  const secondary = platform === "mac" ? chord.ctrl : chord.meta;
  let value = code;
  if (primary) value |= api.KeyMod.CtrlCmd;
  if (secondary) value |= api.KeyMod.WinCtrl;
  if (chord.shift) value |= api.KeyMod.Shift;
  if (chord.alt) value |= api.KeyMod.Alt;
  return value;
}

/** Monaco numeric keybinding for a chord sequence; null for double modifiers or unmappable keys. */
export function monacoKeybinding(sequence: KeySequence, platform: KeyboardPlatform, api: MonacoKeyApi): number | null {
  if (sequence.kind !== "chords" || !sequence.chords.length || sequence.chords.length > 2) return null;
  const first = monacoChord(sequence.chords[0]!, platform, api);
  if (first === null) return null;
  if (sequence.chords.length === 1) return first;
  const second = monacoChord(sequence.chords[1]!, platform, api);
  return second === null ? null : api.chord(first, second);
}

const QUOTE_SAFE = /^[A-Za-z0-9._-]*$/;

/**
 * Monaco expression for one MinCode clause: `editorTextFocus` for
 * `editor.focus`, a `mincodeEditorKind` / `isInDiffEditor` test for
 * `editor.kind`; null when Monaco has no equivalent context key.
 */
export function translateWhenClauseForMonaco(clause: WhenClause): string | null {
  const equal = clause.op === "" ? null : (clause.op === "==") !== clause.negate;
  if (clause.key === "editor.focus" && clause.op === "" && !clause.negate) return "editorTextFocus";
  if (clause.key === "editor.kind" && equal !== null && QUOTE_SAFE.test(clause.value)) {
    if (clause.value === "diff") return equal ? "isInDiffEditor" : "!isInDiffEditor";
    return `mincodeEditorKind ${equal ? "==" : "!="} '${clause.value}'`;
  }
  return null;
}

/** Translates MinCode when clauses to a Monaco when expression, or returns the offending clause. */
export function translateWhenForMonaco(clauses: readonly WhenClause[]): { ok: true; when: string } | { ok: false; detail: string } {
  const parts: string[] = [];
  for (const clause of clauses) {
    const translated = translateWhenClauseForMonaco(clause);
    if (translated === null) {
      return { ok: false, detail: `${clause.negate ? "!" : ""}${clause.key}${clause.op ? ` ${clause.op} "${clause.value}"` : ""}` };
    }
    parts.push(translated);
  }
  if (!parts.includes("editorTextFocus")) parts.unshift("editorTextFocus");
  return { ok: true, when: [...new Set(parts)].join(" && ") };
}

function wholeEditor(binding: ResolvedKeybinding): boolean {
  return binding.clauses.every((clause) => clause.key === "editor.focus" && clause.op === "" && !clause.negate);
}

function collides(a: KeySequence, b: KeySequence): boolean {
  if (a.kind !== "chords" || b.kind !== "chords") return false;
  const length = Math.min(a.chords.length, b.chords.length);
  for (let index = 0; index < length; index++) {
    if (chordId(a.chords[index]!) !== chordId(b.chords[index]!)) return false;
  }
  return true;
}

export function buildMonacoKeybindingRules(input: MonacoRulesInput): MonacoRulesResult {
  const { platform, resolution, api } = input;
  const defaults = monacoDefaultBindings(platform)
    .map((binding) => ({ binding, sequence: parseKeySequence(binding.key, platform) }))
    .filter((entry): entry is { binding: MonacoDefaultBinding; sequence: KeySequence } => entry.sequence !== null);
  const defaultsByCommand = new Map<string, typeof defaults>();
  for (const entry of defaults) {
    const list = defaultsByCommand.get(entry.binding.command) ?? [];
    list.push(entry);
    defaultsByCommand.set(entry.binding.command, list);
  }

  // Effective bindings of Monaco-handled catalog commands, grouped by Monaco id.
  const effective = new Map<string, ResolvedKeybinding[]>();
  for (const binding of resolution.bindings) {
    const monacoCommand = getKeybindingCommand(binding.command)?.monacoCommand;
    if (!monacoCommand) continue;
    const list = effective.get(monacoCommand) ?? [];
    list.push(binding);
    effective.set(monacoCommand, list);
  }

  const changed: string[] = [];
  const candidates = new Set<string>([...effective.keys(), ...defaultsByCommand.keys()]);
  for (const monacoCommand of candidates) {
    const catalog = getKeybindingCommand(`editor:${monacoCommand}`);
    if (!catalog) continue; // non-catalog Monaco commands are only touched by collision removal
    const bindings = effective.get(monacoCommand) ?? [];
    const defaultCount = defaultsByCommand.get(monacoCommand)?.length ?? 0;
    const survivingDefaults = bindings.filter((binding) => binding.layer === "default").length;
    if (bindings.some((binding) => binding.layer !== "default") || survivingDefaults < defaultCount) changed.push(monacoCommand);
  }

  const removals: MonacoKeybindingRule[] = [];
  const additions: MonacoKeybindingRule[] = [];
  const skipped: MonacoRuleSkip[] = [];
  const removed = new Set<string>();
  const removeDefault = (entry: { binding: MonacoDefaultBinding; sequence: KeySequence }) => {
    const id = `${entry.binding.command}\u0000${sequenceId(entry.sequence)}`;
    if (removed.has(id)) return;
    const keybinding = monacoKeybinding(entry.sequence, platform, api);
    if (keybinding === null) return;
    removed.add(id);
    removals.push({ keybinding, command: `-${entry.binding.command}` });
  };

  // Keys a preset/user rule gives to another editor command for the whole
  // editor (JetBrains ⌘D → duplicateSelection): the default binding there is
  // removed and must not be re-added, or Monaco would still list it.
  const takenKeys = new Map<string, Set<string>>();
  for (const [monacoCommand, bindings] of effective) {
    for (const binding of bindings) {
      if (binding.layer === "default" || !wholeEditor(binding)) continue;
      const owners = takenKeys.get(binding.sequenceKey) ?? new Set<string>();
      owners.add(monacoCommand);
      takenKeys.set(binding.sequenceKey, owners);
    }
  }
  const takenByOther = (binding: ResolvedKeybinding, monacoCommand: string) => {
    const owners = takenKeys.get(binding.sequenceKey);
    return Boolean(owners && [...owners].some((owner) => owner !== monacoCommand));
  };

  const removeCollidingDefaults = (binding: ResolvedKeybinding, monacoCommand: string) => {
    if (binding.layer === "default" || !wholeEditor(binding)) return;
    for (const entry of defaults) {
      if (entry.binding.command === monacoCommand) continue;
      if (collides(entry.sequence, binding.sequence)) removeDefault(entry);
    }
  };

  for (const monacoCommand of changed) {
    for (const entry of defaultsByCommand.get(monacoCommand) ?? []) removeDefault(entry);
    for (const binding of effective.get(monacoCommand) ?? []) {
      if (binding.layer === "default" && takenByOther(binding, monacoCommand)) continue;
      if (input.editorModeOwns?.(binding.sequence)) {
        // The editor mode handles the key, but the Monaco default it displaced must stay removed.
        removeCollidingDefaults(binding, monacoCommand);
        skipped.push({ binding, reason: "editor-mode", detail: binding.sequenceKey });
        continue;
      }
      const keybinding = monacoKeybinding(binding.sequence, platform, api);
      if (keybinding === null) {
        skipped.push({ binding, reason: "key", detail: binding.sequenceKey });
        continue;
      }
      let when: string;
      // A preset/user rule that re-states a Monaco default (same command and
      // key, no narrower when) keeps Monaco's own when, e.g. `!editorReadonly`
      // or the find widget's `editorFocus || editorIsOpen`.
      const defaultEntry =
        binding.layer === "default" || wholeEditor(binding)
          ? defaultsByCommand.get(monacoCommand)?.find((entry) => sequenceId(entry.sequence) === binding.sequenceKey)
          : undefined;
      if (defaultEntry?.binding.monacoWhen) {
        when = defaultEntry.binding.monacoWhen;
      } else {
        const translated = translateWhenForMonaco(binding.clauses);
        if (!translated.ok) {
          skipped.push({ binding, reason: "when", detail: translated.detail });
          continue;
        }
        when = translated.when;
      }
      additions.push({ keybinding, command: monacoCommand, when, order: binding.order });
      removeCollidingDefaults(binding, monacoCommand);
    }
  }
  // Monaco's resolver lets the later dynamic rule win, so additions follow resolver order.
  additions.sort((a, b) => (a.order ?? 0) - (b.order ?? 0));
  // A preset re-stating a default yields the same rule twice; keep the later one.
  const seen = new Set<string>();
  const unique = additions
    .reverse()
    .filter((rule) => {
      const id = `${rule.keybinding}\u0000${rule.command}\u0000${rule.when ?? ""}`;
      if (seen.has(id)) return false;
      seen.add(id);
      return true;
    })
    .reverse();
  return { rules: [...removals, ...unique], skipped, changedCommands: changed };
}
