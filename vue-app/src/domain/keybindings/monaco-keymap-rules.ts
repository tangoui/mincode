/**
 * The complete Monaco rule set for the active keymap (pure; Monaco enums are
 * injected). services/monaco-keymap.ts applies it with one global
 * `monaco.editor.addKeybindingRules` call, because Monaco's keybinding service
 * is a process-wide singleton.
 *
 * The set is, in order:
 *  1. removal rules (`-command`) for Monaco defaults the keymap rebinds or
 *     takes over (monaco-rules.ts);
 *  2. additions sorted by resolver order (Monaco lets the later rule win):
 *     - preset / user bindings of Monaco editor commands (monaco-rules.ts);
 *     - editor host commands (save, save all, run query) on
 *       `mincode.editor.save` / `mincode.editor.run`, which the service
 *       dispatches to the focused editor;
 *     - mirrored application bindings (⌘K palette, ⌘I AI, ⇧⌘L lock, plugin
 *       commands …) on `mincode.app.run` with `commandArgs: {id}`.
 *
 * The window dispatcher skips events from Monaco (`monacoMode: "skip"`), so
 * mirroring is the only way an application chord reaches its command while
 * an editor has focus. A mirrored rule's Monaco `when` carries the clauses
 * Monaco can evaluate (editor focus, `editor.kind`); every other clause of
 * the binding travels in `commandArgs.when` and is checked at execution time.
 * When that check fails, `commandArgs.fallback` names the Monaco command the
 * rule shadowed on the same key, so the key keeps its editor meaning.
 */
import type { WhenClause } from "@/domain/commands/when";
import { getKeybindingCommand, isPluginCommandId } from "@/domain/keybindings/catalog";
import { parseKeySequence } from "@/domain/keybindings/keys";
import { admitsBinding } from "@/domain/keybindings/matcher";
import { monacoDefaultBindings } from "@/domain/keybindings/monaco-defaults";
import {
  buildMonacoKeybindingRules,
  monacoKeybinding,
  translateWhenClauseForMonaco,
  type MonacoKeybindingRule,
  type MonacoRuleSkip,
  type MonacoRulesInput,
} from "@/domain/keybindings/monaco-rules";
import { formatWhenClauses, whenClausesDisjoint, type ResolvedKeybinding } from "@/domain/keybindings/resolver";

/** Global Monaco command that runs a MinCode application command (`commandArgs.id`). */
export const MONACO_APP_COMMAND = "mincode.app.run";
/** Global Monaco command that saves the focused MinCode editor. */
export const MONACO_EDITOR_SAVE_COMMAND = "mincode.editor.save";
/** Global Monaco command that runs the focused editor's primary action (SQL: run query). */
export const MONACO_EDITOR_RUN_COMMAND = "mincode.editor.run";
/** Monaco context key set by every MinCode text editor (`editor.kind` in when clauses). */
export const MONACO_EDITOR_KIND_CONTEXT_KEY = "mincodeEditorKind";

/** Catalog commands implemented by the focused editor instead of the command registry. */
export const MONACO_EDITOR_HOST_COMMANDS: Readonly<Record<string, string>> = Object.freeze({
  "editor:save": MONACO_EDITOR_SAVE_COMMAND,
  // Until a workspace save-all handler is registered, save-all saves the focused editor.
  "database:run-query": MONACO_EDITOR_RUN_COMMAND,
});

export interface MonacoCommandArgs {
  /** MinCode command id. */
  id: string;
  /** Clauses Monaco cannot evaluate, checked against the MinCode context at execution. */
  when?: string;
  /** Monaco command this rule shadows on the same key; run when `id` cannot. */
  fallback?: string;
}

export interface MonacoKeymapRule extends MonacoKeybindingRule {
  commandArgs?: MonacoCommandArgs;
}

export interface MonacoKeymapResult {
  rules: MonacoKeymapRule[];
  skipped: MonacoRuleSkip[];
  /** Monaco commands whose defaults were replaced. */
  changedCommands: string[];
  /** MinCode commands mirrored into Monaco (app and editor host commands). */
  mirroredCommands: string[];
}

const truthy = (key: string): WhenClause => ({ key, op: "", value: "", negate: false });
const falsy = (key: string): WhenClause => ({ key, op: "", value: "", negate: true });

/**
 * What is known about the MinCode context whenever Monaco handles a key: the
 * editor has focus, so the terminal, plain inputs and the project tree do
 * not. The AI panel's keys stay with the panel's own fields (its Monaco diff
 * is a read-only review), so they are never mirrored either.
 */
export const MONACO_FOCUS_CLAUSES: readonly WhenClause[] = Object.freeze([
  truthy("editor.focus"),
  falsy("terminal.focus"),
  falsy("terminal.selection"),
  falsy("input.focus"),
  falsy("tree.selection"),
  falsy("ai.panel.focus"),
]);

function staticallyTrue(clause: WhenClause): boolean {
  return MONACO_FOCUS_CLAUSES.some(
    (known) => known.key === clause.key && clause.op === "" && known.negate === clause.negate,
  );
}

function mirrorable(binding: ResolvedKeybinding): boolean {
  const host = MONACO_EDITOR_HOST_COMMANDS[binding.command];
  if (host) return true;
  if (isPluginCommandId(binding.command)) return true;
  const entry = getKeybindingCommand(binding.command);
  return Boolean(entry && entry.handler === "app" && entry.available);
}

/**
 * Shifted clipboard / select-all letters (⇧⌘A, ⇧⌘C …) are text-editing
 * chords for plain inputs, but Monaco binds nothing to them, so application
 * bindings there (JetBrains ⇧⌘A Find Action) are mirrored. ⇧⌘Z / ⇧⌘Y stay
 * with the editor (redo).
 */
const MONACO_FREE_SHIFTED_LETTERS = new Set(["KeyA", "KeyC", "KeyV", "KeyX"]);

function monacoAdmits(binding: ResolvedKeybinding, platform: MonacoRulesInput["platform"]): boolean {
  if (binding.sequence.kind !== "chords") return false;
  const chord = binding.sequence.chords[0] ?? null;
  if (admitsBinding(binding, chord, "monaco", platform)) return true;
  if (!chord) return false;
  const primary = platform === "mac" ? chord.meta && !chord.ctrl : chord.ctrl && !chord.meta;
  return primary && chord.shift && !chord.alt && MONACO_FREE_SHIFTED_LETTERS.has(chord.code);
}

export function buildMonacoKeymapRules(input: MonacoRulesInput): MonacoKeymapResult {
  const { platform, resolution, api } = input;
  const editor = buildMonacoKeybindingRules(input);
  const removals = editor.rules.filter((rule) => rule.command.startsWith("-"));
  const additions: MonacoKeymapRule[] = editor.rules.filter((rule) => !rule.command.startsWith("-"));
  const skipped = [...editor.skipped];

  // Monaco commands still handling each single-chord key in a text editor,
  // lowest priority first: surviving defaults, then keymap additions.
  const removed = new Set(removals.map((rule) => `${rule.command.slice(1)}\u0000${rule.keybinding}`));
  const editorCommandsByKey = new Map<number, string[]>();
  const noteEditorCommand = (keybinding: number, command: string) => {
    const list = editorCommandsByKey.get(keybinding) ?? [];
    list.push(command);
    editorCommandsByKey.set(keybinding, list);
  };
  for (const binding of monacoDefaultBindings(platform)) {
    const sequence = parseKeySequence(binding.key, platform);
    const keybinding = sequence ? monacoKeybinding(sequence, platform, api) : null;
    if (keybinding === null || removed.has(`${binding.command}\u0000${keybinding}`)) continue;
    noteEditorCommand(keybinding, binding.command);
  }
  for (const rule of additions) {
    if (!rule.command.startsWith("mincode.diff.")) noteEditorCommand(rule.keybinding, rule.command);
  }

  const mirrored = new Set<string>();
  for (const binding of resolution.bindings) {
    if (binding.sequence.kind !== "chords" || !mirrorable(binding)) continue;
    // Bindings that can never apply inside an editor (e.g. `!editor.focus`, `terminal.focus`).
    if (whenClausesDisjoint(binding.clauses, MONACO_FOCUS_CLAUSES)) continue;
    // The same focus admission the dispatcher applies to text fields: Monaco keeps text-editing chords.
    if (!monacoAdmits(binding, platform)) continue;
    if (input.editorModeOwns?.(binding.sequence)) {
      skipped.push({ binding, reason: "editor-mode", detail: binding.sequenceKey });
      continue;
    }
    const keybinding = monacoKeybinding(binding.sequence, platform, api);
    if (keybinding === null) {
      skipped.push({ binding, reason: "key", detail: binding.sequenceKey });
      continue;
    }
    const monacoWhen = ["editorFocus"];
    const runtime: WhenClause[] = [];
    for (const clause of binding.clauses) {
      if (staticallyTrue(clause)) continue;
      const translated = translateWhenClauseForMonaco(clause);
      if (translated === "editorTextFocus") continue; // implied by editorFocus
      if (translated !== null) monacoWhen.push(translated);
      else runtime.push(clause);
    }
    const args: MonacoCommandArgs = { id: binding.command };
    if (runtime.length) args.when = formatWhenClauses(runtime);
    const shadowed = binding.sequence.chords.length === 1 ? editorCommandsByKey.get(keybinding) : undefined;
    const fallback = shadowed?.[shadowed.length - 1];
    if (fallback) args.fallback = fallback;
    additions.push({
      keybinding,
      command: MONACO_EDITOR_HOST_COMMANDS[binding.command] ?? MONACO_APP_COMMAND,
      when: [...new Set(monacoWhen)].join(" && "),
      commandArgs: args,
      order: binding.order,
    });
    mirrored.add(binding.command);
  }

  additions.sort((a, b) => (a.order ?? 0) - (b.order ?? 0));
  return {
    rules: [...removals, ...additions],
    skipped,
    changedCommands: editor.changedCommands,
    mirroredCommands: [...mirrored],
  };
}

