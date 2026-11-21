/**
 * Keybinding resolution: merges the rule layers into one ordered binding list
 * with VS Code semantics, indexes it for the matcher, and detects conflicts.
 *
 * Layers, lowest priority first:
 *   default — Monaco defaults of catalog editor commands (monaco-defaults.ts)
 *   preset  — the active preset chain, `extends` root first
 *   host    — HOST_KEYBINDING_RULES (panel-scoped rules shared by all presets)
 *   user    — user overrides
 * A later rule beats an earlier one. `command: "-<id>"` removes earlier
 * bindings of <id>: with a key only that key (and when, if given), without a
 * key every binding. Pure: no DOM, store or Monaco access.
 */
import { parseWhenExpression, type WhenClause } from "@/domain/commands/when";
import { getKeybindingCommand, isBindableCommandId } from "@/domain/keybindings/catalog";
import {
  chordId,
  parseKeySequence,
  sequenceId,
  type KeyChord,
  type KeySequence,
  type KeyboardPlatform,
  type ModifierName,
} from "@/domain/keybindings/keys";
import { presetRulesForPlatform, type KeybindingRule, type KeymapPresetData, type KeymapPresetRule } from "@/domain/keybindings/preset";
import { reservedKeyReason, type ReservedKeyReason } from "@/domain/keybindings/reserved";

export type KeybindingLayer = "default" | "preset" | "host" | "user";

export interface ResolvedKeybinding {
  command: string;
  sequence: KeySequence;
  /** Canonical sequence id (`shift+meta+p`, `meta+k meta+s`, `shift shift`). */
  sequenceKey: string;
  /** The rule's own when expression (without implicit focus clauses). */
  when?: string;
  /** Effective clauses including the implicit editor/terminal focus clause. */
  clauses: WhenClause[];
  layer: KeybindingLayer;
  /** Global rule order; higher wins. */
  order: number;
  /** The preset that contributed a `preset` layer rule. */
  presetId?: string;
  reserved?: ReservedKeyReason;
}

export interface KeybindingRuleIssue {
  layer: KeybindingLayer;
  index: number;
  command: string;
  message: string;
}

export interface KeybindingResolverInput {
  platform: KeyboardPlatform;
  /** Preset chain, `extends` root first. */
  presets: readonly KeymapPresetData[];
  defaults?: readonly KeybindingRule[];
  host?: readonly KeymapPresetRule[];
  user?: readonly KeybindingRule[];
}

export interface KeybindingResolution {
  platform: KeyboardPlatform;
  /** Effective bindings in ascending order. */
  bindings: ResolvedKeybinding[];
  /** Chord bindings indexed by the id of their first chord. */
  byFirstChord: Map<string, ResolvedKeybinding[]>;
  /** Double-modifier bindings by modifier. */
  byDoubleModifier: Map<ModifierName, ResolvedKeybinding[]>;
  byCommand: Map<string, ResolvedKeybinding[]>;
  issues: KeybindingRuleIssue[];
}

/** Commands whose handler needs an implicit focus clause. */
function implicitClause(command: string): WhenClause | null {
  const entry = getKeybindingCommand(command);
  if (entry?.handler === "monaco") return { key: "editor.focus", op: "", value: "", negate: false };
  if (entry?.handler === "terminal") return { key: "terminal.focus", op: "", value: "", negate: false };
  return null;
}

export function formatWhenClauses(clauses: readonly WhenClause[]): string {
  return clauses
    .map((clause) => {
      const prefix = clause.negate ? "!" : "";
      return clause.op ? `${prefix}${clause.key} ${clause.op} "${clause.value}"` : `${prefix}${clause.key}`;
    })
    .join(" && ");
}

function normalizedWhen(when: string | undefined): string {
  if (!when?.trim()) return "";
  return formatWhenClauses(parseWhenExpression(when));
}

export function resolveKeybindings(input: KeybindingResolverInput): KeybindingResolution {
  const { platform } = input;
  const bindings: ResolvedKeybinding[] = [];
  const issues: KeybindingRuleIssue[] = [];
  let order = 0;

  const apply = (rule: KeybindingRule, layer: KeybindingLayer, index: number, presetId?: string) => {
    const removal = rule.command.startsWith("-");
    const command = removal ? rule.command.slice(1) : rule.command;
    const issue = (message: string) => issues.push({ layer, index, command: rule.command, message });
    if (!isBindableCommandId(command)) return issue("unknown command");
    let sequence: KeySequence | null = null;
    if (rule.key !== undefined) {
      sequence = parseKeySequence(rule.key, platform);
      if (!sequence) return issue(`invalid key ${JSON.stringify(rule.key)}`);
    } else if (!removal) {
      return issue("missing key");
    }
    let clauses: WhenClause[] = [];
    let whenKey = "";
    if (rule.when?.trim()) {
      try {
        clauses = parseWhenExpression(rule.when);
        whenKey = formatWhenClauses(clauses);
      } catch (error) {
        return issue(error instanceof Error ? error.message : String(error));
      }
    }
    if (removal) {
      const target = sequence ? sequenceId(sequence) : null;
      for (let position = bindings.length - 1; position >= 0; position--) {
        const binding = bindings[position]!;
        if (binding.command !== command) continue;
        if (target !== null && binding.sequenceKey !== target) continue;
        if (target !== null && whenKey && normalizedWhen(binding.when) !== whenKey) continue;
        bindings.splice(position, 1);
      }
      return;
    }
    const implicit = implicitClause(command);
    const effective = implicit && !clauses.some((clause) => clause.key === implicit.key) ? [implicit, ...clauses] : clauses;
    const resolved: ResolvedKeybinding = {
      command,
      sequence: sequence!,
      sequenceKey: sequenceId(sequence!),
      clauses: effective,
      layer,
      order: order++,
    };
    if (rule.when?.trim()) resolved.when = rule.when.trim();
    if (presetId) resolved.presetId = presetId;
    const reserved = reservedKeyReason(sequence!, platform);
    if (reserved) resolved.reserved = reserved;
    bindings.push(resolved);
  };

  (input.defaults ?? []).forEach((rule, index) => apply(rule, "default", index));
  for (const preset of input.presets) {
    presetRulesForPlatform(preset.rules, platform).forEach((rule, index) => apply(rule, "preset", index, preset.id));
  }
  presetRulesForPlatform(input.host ?? [], platform).forEach((rule, index) => apply(rule, "host", index));
  (input.user ?? []).forEach((rule, index) => apply(rule, "user", index));

  const byFirstChord = new Map<string, ResolvedKeybinding[]>();
  const byDoubleModifier = new Map<ModifierName, ResolvedKeybinding[]>();
  const byCommand = new Map<string, ResolvedKeybinding[]>();
  for (const binding of bindings) {
    if (binding.sequence.kind === "double") {
      pushTo(byDoubleModifier, binding.sequence.modifier, binding);
    } else {
      pushTo(byFirstChord, chordId(binding.sequence.chords[0]!), binding);
    }
    pushTo(byCommand, binding.command, binding);
  }
  return { platform, bindings, byFirstChord, byDoubleModifier, byCommand, issues };
}

function pushTo<K>(map: Map<K, ResolvedKeybinding[]>, key: K, binding: ResolvedKeybinding) {
  const list = map.get(key);
  if (list) list.push(binding);
  else map.set(key, [binding]);
}

/** Effective bindings of a command, highest priority first. */
export function bindingsForCommand(resolution: KeybindingResolution, command: string): ResolvedKeybinding[] {
  return [...(resolution.byCommand.get(command) ?? [])].reverse();
}

/** The binding shown as a command's label: the highest-priority one. */
export function primaryBinding(resolution: KeybindingResolution, command: string): ResolvedKeybinding | undefined {
  const list = resolution.byCommand.get(command);
  return list?.[list.length - 1];
}

export type KeybindingLookup =
  | { kind: "none" }
  | { kind: "execute"; binding: ResolvedKeybinding }
  | { kind: "pending"; binding: ResolvedKeybinding };

/**
 * Finds the binding for the pressed chords (one, or a pending prefix plus the
 * new chord). The highest-order eligible binding whose sequence starts with
 * the pressed chords wins: an exact match executes, a longer one enters (or
 * stays in) chord-pending mode.
 */
export function lookupKeybinding(
  resolution: KeybindingResolution,
  pressed: readonly KeyChord[],
  eligible: (binding: ResolvedKeybinding) => boolean,
): KeybindingLookup {
  if (!pressed.length) return { kind: "none" };
  const pressedIds = pressed.map(chordId);
  const candidates = resolution.byFirstChord.get(pressedIds[0]!) ?? [];
  for (let position = candidates.length - 1; position >= 0; position--) {
    const binding = candidates[position]!;
    if (binding.sequence.kind !== "chords") continue;
    const chords = binding.sequence.chords;
    if (chords.length < pressed.length) continue;
    if (!pressedIds.every((id, index) => chordId(chords[index]!) === id)) continue;
    if (!eligible(binding)) continue;
    return chords.length === pressed.length ? { kind: "execute", binding } : { kind: "pending", binding };
  }
  return { kind: "none" };
}

// ---------------------------------------------------------------------------
// when-disjointness and conflicts
// ---------------------------------------------------------------------------

type Literal = { key: string; kind: "truthy" | "falsy" | "eq" | "ne"; value: string };

function literal(clause: WhenClause): Literal {
  if (clause.op === "") return { key: clause.key, kind: clause.negate ? "falsy" : "truthy", value: "" };
  const equal = (clause.op === "==") !== clause.negate;
  return { key: clause.key, kind: equal ? "eq" : "ne", value: clause.value };
}

/** Context keys that are never true at the same time. */
const MUTUALLY_EXCLUSIVE_KEYS: readonly (readonly string[])[] = [
  // One focus class per event (context.ts keybindingFocusClass).
  ["editor.focus", "terminal.focus", "input.focus"],
  // The project tree provider is false inside inputs, Monaco and xterm.
  ["tree.selection", "editor.focus"],
  ["tree.selection", "terminal.focus"],
  ["tree.selection", "input.focus"],
];

function contradicts(a: Literal, b: Literal): boolean {
  if (a.key !== b.key) {
    return (
      a.kind === "truthy" &&
      b.kind === "truthy" &&
      MUTUALLY_EXCLUSIVE_KEYS.some((group) => group.includes(a.key) && group.includes(b.key))
    );
  }
  const pair = (x: Literal["kind"], y: Literal["kind"]) => (a.kind === x && b.kind === y) || (a.kind === y && b.kind === x);
  if (pair("truthy", "falsy")) return true;
  if (a.kind === "eq" && b.kind === "eq") return a.value !== b.value;
  if (pair("eq", "ne")) return a.value === b.value;
  if (pair("eq", "falsy")) return (a.kind === "eq" ? a.value : b.value) !== "";
  if (pair("eq", "truthy")) return (a.kind === "eq" ? a.value : b.value) === "";
  return false;
}

/** True when no context can satisfy both clause lists. */
export function whenClausesDisjoint(a: readonly WhenClause[], b: readonly WhenClause[]): boolean {
  const left = a.map(literal);
  const right = b.map(literal);
  return left.some((x) => right.some((y) => contradicts(x, y)));
}

export interface KeybindingConflict {
  kind: "same" | "prefix";
  first: ResolvedKeybinding;
  second: ResolvedKeybinding;
}

function sequencesCollide(a: KeySequence, b: KeySequence): "same" | "prefix" | null {
  if (a.kind === "double" || b.kind === "double") {
    return a.kind === "double" && b.kind === "double" && a.modifier === b.modifier ? "same" : null;
  }
  const length = Math.min(a.chords.length, b.chords.length);
  for (let index = 0; index < length; index++) {
    if (chordId(a.chords[index]!) !== chordId(b.chords[index]!)) return null;
  }
  return a.chords.length === b.chords.length ? "same" : "prefix";
}

function clauseSet(binding: ResolvedKeybinding): Set<string> {
  return new Set(binding.clauses.map((clause) => formatWhenClauses([clause])));
}

/** Context keys that only say where the focus is (no application state). */
const FOCUS_CONTEXT_KEYS: ReadonlySet<string> = new Set(["editor.focus", "terminal.focus", "input.focus", "dialog.focus", "ai.panel.focus", "tree.selection", "text.selection", "terminal.selection"]);

/**
 * A later single-chord binding that holds only while some application state
 * does (`debug.active`) over a single-chord Monaco default on the same key: the default
 * still applies whenever that state does not hold (the Monaco mirror gives
 * the key back through `commandArgs.fallback`), so this is layering, not a
 * conflict. A when made only of focus keys may hide the default everywhere
 * in the editor and is still reported.
 */
function shadowsDefaultWhileStateHolds(narrow: ResolvedKeybinding, broad: ResolvedKeybinding): boolean {
  return (
    broad.layer === "default" &&
    narrow.layer !== "default" &&
    narrow.order > broad.order &&
    narrow.sequence.kind === "chords" &&
    narrow.sequence.chords.length === 1 &&
    // The mirror only records a fallback for single-chord defaults; a chord default would be eaten.
    broad.sequence.kind === "chords" &&
    broad.sequence.chords.length === 1 &&
    narrow.clauses.some((clause) => !clause.negate && clause.op === "" && !FOCUS_CONTEXT_KEYS.has(clause.key))
  );
}

/**
 * True when `narrow` deliberately specialises `broad`: its when is strictly
 * narrower and it wins wherever it applies. That holds when it has the higher
 * resolver order, or when it is a terminal command scoped to terminal focus
 * (the xterm key handler runs before the window dispatcher).
 */
function specialises(narrow: ResolvedKeybinding, broad: ResolvedKeybinding): boolean {
  const narrowSet = clauseSet(narrow);
  const broadSet = clauseSet(broad);
  const strictlyNarrower = narrowSet.size > broadSet.size && [...broadSet].every((clause) => narrowSet.has(clause));
  if (strictlyNarrower && narrow.order > broad.order) return true;
  if (shadowsDefaultWhileStateHolds(narrow, broad)) return true;
  return (
    narrow.command.startsWith("terminal:") &&
    narrowSet.has("terminal.focus") &&
    !broadSet.has("terminal.focus") &&
    !broad.command.startsWith("terminal:")
  );
}

/**
 * Two bindings conflict when their sequences collide, commands differ, their
 * whens can overlap, and neither is a deliberate narrower override of the other.
 */
export function keybindingsConflict(a: ResolvedKeybinding, b: ResolvedKeybinding): KeybindingConflict["kind"] | null {
  if (a.command === b.command) return null;
  const kind = sequencesCollide(a.sequence, b.sequence);
  if (!kind || whenClausesDisjoint(a.clauses, b.clauses)) return null;
  if (specialises(a, b) || specialises(b, a)) return null;
  return kind;
}

/**
 * Drops default-layer rules a preset (or host rule) has taken over, mirroring
 * the Monaco rule builder: defaults of commands the overriding rules rebind,
 * and defaults on a key an overriding rule of another command claims for the
 * whole editor or everywhere (when ⊆ {editor.focus}).
 */
export function pruneReplacedDefaults(
  defaults: readonly KeybindingRule[],
  overriding: readonly KeybindingRule[],
  platform: KeyboardPlatform,
): KeybindingRule[] {
  const rebound = new Set<string>();
  const claimed = new Map<string, Set<string>>();
  for (const rule of overriding) {
    if (rule.command.startsWith("-") || rule.key === undefined) continue;
    rebound.add(rule.command);
    const sequence = parseKeySequence(rule.key, platform);
    if (!sequence) continue;
    let clauses: WhenClause[];
    try {
      clauses = rule.when?.trim() ? parseWhenExpression(rule.when) : [];
    } catch {
      continue;
    }
    const wholeEditor = clauses.every((clause) => clause.key === "editor.focus" && clause.op === "" && !clause.negate);
    if (!wholeEditor) continue;
    const id = sequenceId(sequence);
    const commands = claimed.get(id) ?? new Set<string>();
    commands.add(rule.command);
    claimed.set(id, commands);
  }
  return defaults.filter((rule) => {
    if (rebound.has(rule.command)) return false;
    const sequence = rule.key === undefined ? null : parseKeySequence(rule.key, platform);
    if (!sequence) return true;
    const owners = claimed.get(sequenceId(sequence));
    return !owners || owners.has(rule.command);
  });
}

/** Every conflicting pair among the given bindings (`first` has the lower order). */
export function findKeybindingConflicts(bindings: readonly ResolvedKeybinding[]): KeybindingConflict[] {
  const conflicts: KeybindingConflict[] = [];
  const groups = new Map<string, ResolvedKeybinding[]>();
  for (const binding of bindings) {
    const group = binding.sequence.kind === "double" ? `double:${binding.sequence.modifier}` : chordId(binding.sequence.chords[0]!);
    pushTo(groups, group, binding);
  }
  for (const group of groups.values()) {
    const sorted = [...group].sort((x, y) => x.order - y.order);
    for (let i = 0; i < sorted.length; i++) {
      for (let j = i + 1; j < sorted.length; j++) {
        const kind = keybindingsConflict(sorted[i]!, sorted[j]!);
        if (kind) conflicts.push({ kind, first: sorted[i]!, second: sorted[j]! });
      }
    }
  }
  return conflicts;
}
