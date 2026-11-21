/**
 * Pure keybinding state machine: chord-pending, double-modifier taps, repeat
 * handling and focus-class admission. Time comes from an injected clock so
 * the behaviour is testable without real timers; the dispatcher owns DOM
 * listeners, execution and the chord hint.
 */
import { evaluateWhenClauses, type PluginContextValues } from "@/domain/commands/when";
import { getKeybindingCommand } from "@/domain/keybindings/catalog";
import type { KeybindingFocusClass } from "@/domain/keybindings/context";
import {
  keyChordFromEvent,
  modifierFromCode,
  type KeyChord,
  type KeyboardEventLike,
  type KeyboardPlatform,
  type ModifierName,
} from "@/domain/keybindings/keys";
import { lookupKeybinding, type KeybindingResolution, type ResolvedKeybinding } from "@/domain/keybindings/resolver";

export const CHORD_TIMEOUT_MS = 3000;
export const DOUBLE_TAP_MAX_PRESS_MS = 300;
export const DOUBLE_TAP_MAX_GAP_MS = 400;
export const COMPOSITION_QUIET_MS = 500;

export interface MatcherKeyEvent extends KeyboardEventLike {
  repeat?: boolean;
  isComposing?: boolean;
  keyCode?: number;
}

export interface MatcherEnvironment {
  focus: KeybindingFocusClass;
  context: PluginContextValues;
  /** Whether the command can run now (a handler is registered). */
  canExecute: (command: string) => boolean;
}

export type MatcherDecision =
  | { kind: "ignore" }
  | { kind: "execute"; binding: ResolvedKeybinding }
  | { kind: "pending"; binding: ResolvedKeybinding; prefix: KeyChord[] }
  | { kind: "cancel"; reason: "escape" | "mismatch" }
  /** Swallow the event without running anything (auto-repeat of a non-repeatable binding). */
  | { kind: "consume" };

export interface KeybindingMatcherOptions {
  now: () => number;
  platform: KeyboardPlatform;
  resolution: () => KeybindingResolution;
  /** Double-modifier bindings are active (store option). */
  doubleModifier: () => boolean;
  /**
   * Monaco focus handling: "input" treats Monaco like a text field; "skip"
   * (the dispatcher since phase 3) leaves chords to services/monaco-keymap.ts,
   * which mirrors application bindings into Monaco's keybinding service.
   */
  monacoMode?: () => "input" | "skip";
}

/** Commands that may fire while the terminal has focus (otherwise the shell gets the key). */
export const TERMINAL_PASSTHROUGH_COMMANDS: ReadonlySet<string> = new Set([
  "builtin:command-palette",
  "builtin:quick-open",
  "builtin:search-everywhere",
  "workbench:show-view.terminal",
  "workbench:next-tab",
  "workbench:prev-tab",
  "workbench:close-tab",
  "builtin:settings",
  "builtin:lock-app",
  "builtin:ai",
]);

const MAC_EMACS_CODES = new Set(["KeyA", "KeyB", "KeyD", "KeyE", "KeyF", "KeyH", "KeyK", "KeyN", "KeyP"]);
const EDIT_LETTER_CODES = new Set(["KeyA", "KeyC", "KeyV", "KeyX", "KeyZ", "KeyY"]);
const ARROW_CODES = new Set(["ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight"]);

function isFunctionKey(code: string): boolean {
  return /^F([1-9]|1[0-9]|2[0-4])$/.test(code);
}

/** Chords a text field needs for editing; bindings never take them from an input unless they opt in. */
export function isTextEditingChord(chord: KeyChord, platform: KeyboardPlatform): boolean {
  const primary = platform === "mac" ? chord.meta : chord.ctrl;
  const anyModifier = chord.ctrl || chord.alt || chord.meta;
  if (primary && !chord.alt && EDIT_LETTER_CODES.has(chord.code)) return true;
  if (ARROW_CODES.has(chord.code) && (anyModifier || chord.shift)) return true;
  if ((chord.code === "Backspace" || chord.code === "Delete") && anyModifier) return true;
  if (chord.code === "Home" || chord.code === "End") return true;
  if (platform === "mac" && chord.ctrl && !chord.meta && !chord.alt && MAC_EMACS_CODES.has(chord.code)) return true;
  return false;
}

function clausesName(binding: ResolvedKeybinding, key: string): boolean {
  return binding.clauses.some((clause) => clause.key === key && !clause.negate);
}

/** Focus-class admission for a binding (the chord is its first chord). */
export function admitsBinding(
  binding: ResolvedKeybinding,
  chord: KeyChord | null,
  focus: KeybindingFocusClass,
  platform: KeyboardPlatform,
): boolean {
  const command = getKeybindingCommand(binding.command);
  switch (focus) {
    case "other":
      return true;
    case "dialog":
      return binding.command === "builtin:lock-app" || clausesName(binding, "dialog.focus");
    case "terminal":
      if (TERMINAL_PASSTHROUGH_COMMANDS.has(binding.command)) return true;
      return platform === "mac" && Boolean(chord?.meta);
    case "input":
    case "monaco": {
      if (command?.allowInInput || clausesName(binding, "input.focus")) return true;
      if (focus === "monaco" && clausesName(binding, "editor.focus")) return !chord || !isTextEditingChord(chord, platform);
      if (!chord) return true;
      if (isTextEditingChord(chord, platform)) return false;
      return chord.ctrl || chord.meta || isFunctionKey(chord.code);
    }
  }
}

function isImeEvent(event: MatcherKeyEvent): boolean {
  return Boolean(event.isComposing) || event.keyCode === 229 || event.key === "Process";
}

export class KeybindingMatcher {
  private readonly options: KeybindingMatcherOptions;
  private pendingPrefix: KeyChord[] | null = null;
  private pendingSince = 0;
  private compositionAt = Number.NEGATIVE_INFINITY;
  private tapModifier: ModifierName | null = null;
  private tapDownAt = 0;
  private tapClean = false;
  private lastTap: { modifier: ModifierName; at: number } | null = null;
  private secondTapStarted = false;

  constructor(options: KeybindingMatcherOptions) {
    this.options = options;
  }

  /** Pending chord prefix, or null. */
  get pending(): readonly KeyChord[] | null {
    return this.pendingPrefix;
  }

  cancelPending(): boolean {
    const had = this.pendingPrefix !== null;
    this.pendingPrefix = null;
    return had;
  }

  /** True when a pending chord has outlived CHORD_TIMEOUT_MS (clears it). */
  expirePending(): boolean {
    if (this.pendingPrefix && this.options.now() - this.pendingSince >= CHORD_TIMEOUT_MS) {
      this.pendingPrefix = null;
      return true;
    }
    return false;
  }

  /** Records IME composition activity; double-modifier taps are suppressed shortly after. */
  noteComposition(): void {
    this.compositionAt = this.options.now();
    this.resetTaps();
  }

  resetTaps(): void {
    this.tapModifier = null;
    this.tapClean = false;
    this.lastTap = null;
    this.secondTapStarted = false;
  }

  private eligible(env: MatcherEnvironment, chord: KeyChord | null) {
    const monacoMode = this.options.monacoMode?.() ?? "input";
    return (binding: ResolvedKeybinding) => {
      // Chords reach Monaco-focused commands through monaco-keymap's mirrored
      // rules; double-modifier taps cannot be expressed there and stay here.
      if (env.focus === "monaco" && monacoMode === "skip" && binding.sequence.kind === "chords") return false;
      if (!admitsBinding(binding, chord, env.focus, this.options.platform)) return false;
      if (!evaluateWhenClauses(binding.clauses, env.context)) return false;
      return env.canExecute(binding.command);
    };
  }

  /**
   * Capture phase: continues a pending chord. Returns "ignore" when no chord
   * is pending (or it timed out), so the event proceeds to the bubble phase.
   */
  handlePendingKeydown(event: MatcherKeyEvent, env: MatcherEnvironment): MatcherDecision {
    if (!this.pendingPrefix) return { kind: "ignore" };
    if (this.expirePending()) return { kind: "ignore" };
    if (isImeEvent(event)) return { kind: "ignore" };
    const chord = keyChordFromEvent(event);
    if (!chord) return { kind: "ignore" }; // bare modifier while building the second chord
    const prefix = this.pendingPrefix;
    this.pendingPrefix = null;
    if (chord.code === "Escape" && !chord.ctrl && !chord.alt && !chord.meta && !chord.shift) {
      return { kind: "cancel", reason: "escape" };
    }
    const result = lookupKeybinding(this.options.resolution(), [...prefix, chord], this.eligible(env, prefix[0] ?? null));
    if (result.kind === "execute") return { kind: "execute", binding: result.binding };
    if (result.kind === "pending") {
      this.pendingPrefix = [...prefix, chord];
      this.pendingSince = this.options.now();
      return { kind: "pending", binding: result.binding, prefix: this.pendingPrefix };
    }
    return { kind: "cancel", reason: "mismatch" };
  }

  /** Bubble phase: first chord. */
  handleKeydown(event: MatcherKeyEvent, env: MatcherEnvironment): MatcherDecision {
    if (isImeEvent(event)) return { kind: "ignore" };
    const chord = keyChordFromEvent(event);
    if (!chord) return { kind: "ignore" };
    const result = lookupKeybinding(this.options.resolution(), [chord], this.eligible(env, chord));
    if (result.kind === "none") return { kind: "ignore" };
    if (event.repeat && !(result.kind === "execute" && getKeybindingCommand(result.binding.command)?.repeatable)) {
      return { kind: "consume" };
    }
    if (result.kind === "execute") return { kind: "execute", binding: result.binding };
    this.pendingPrefix = [chord];
    this.pendingSince = this.options.now();
    return { kind: "pending", binding: result.binding, prefix: this.pendingPrefix };
  }

  /**
   * Capture phase, keydown and keyup: detects a double tap of a lone
   * modifier. Never consumes events; returns "execute" on the second keyup.
   */
  handleModifierEvent(type: "keydown" | "keyup", event: MatcherKeyEvent, env: MatcherEnvironment): MatcherDecision {
    const now = this.options.now();
    if (isImeEvent(event) || now - this.compositionAt < COMPOSITION_QUIET_MS) {
      this.resetTaps();
      return { kind: "ignore" };
    }
    const modifier = event.code ? modifierFromCode(event.code) : null;
    if (type === "keydown") {
      if (event.repeat) {
        if (modifier && modifier === this.tapModifier) this.tapClean = false;
        return { kind: "ignore" };
      }
      const others = (["ctrl", "shift", "alt", "meta"] as const).filter(
        (name) => name !== modifier && event[`${name}Key` as const],
      );
      if (!modifier || others.length) {
        this.resetTaps();
        return { kind: "ignore" };
      }
      if (this.lastTap && (this.lastTap.modifier !== modifier || now - this.lastTap.at > DOUBLE_TAP_MAX_GAP_MS)) {
        this.lastTap = null;
      }
      this.secondTapStarted = this.lastTap !== null;
      this.tapModifier = modifier;
      this.tapDownAt = now;
      this.tapClean = true;
      return { kind: "ignore" };
    }
    // keyup
    if (!modifier || modifier !== this.tapModifier || !this.tapClean || now - this.tapDownAt > DOUBLE_TAP_MAX_PRESS_MS) {
      this.resetTaps();
      return { kind: "ignore" };
    }
    this.tapModifier = null;
    this.tapClean = false;
    if (!this.secondTapStarted) {
      this.lastTap = { modifier, at: now };
      return { kind: "ignore" };
    }
    this.resetTaps();
    if (!this.options.doubleModifier() || env.focus === "dialog") return { kind: "ignore" };
    const candidates = this.options.resolution().byDoubleModifier.get(modifier) ?? [];
    const eligible = this.eligible(env, null);
    for (let position = candidates.length - 1; position >= 0; position--) {
      const binding = candidates[position]!;
      if (eligible(binding)) return { kind: "execute", binding };
    }
    return { kind: "ignore" };
  }
}
