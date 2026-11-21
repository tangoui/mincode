/**
 * The single window-level keyboard dispatcher. It feeds keyboard events to
 * the pure KeybindingMatcher (domain/keybindings/matcher.ts) and runs the
 * matched command through services/command-registry.ts.
 *
 *  - bubble phase `keydown`: ordinary bindings, so focused controls (Monaco,
 *    xterm, Element Plus popups) that handle a key and call preventDefault /
 *    stopPropagation keep it;
 *  - capture phase `keydown`: only the second chord of a pending chord and
 *    double-modifier tracking, which must see the key before any control;
 *  - capture phase `keyup` / composition events: double-modifier and IME.
 *
 * Guards: IME composition (in the matcher), and the
 * settings key recorder (`keybindings.recording`). Phase 2 only executes
 * `handler: "app"` catalog commands and plugin commands with a registered
 * handler; keys of other commands pass through untouched.
 */
import { ElMessage, type MessageHandler } from "element-plus";
import type { PluginContextValues } from "@/domain/commands/when";
import { getKeybindingCommand, isPluginCommandId } from "@/domain/keybindings/catalog";
import { buildKeybindingContext, keybindingFocusClass, type ElementLike } from "@/domain/keybindings/context";
import { formatKeyChord, keyChordFromEvent, type KeyChord } from "@/domain/keybindings/keys";
import {
  CHORD_TIMEOUT_MS,
  KeybindingMatcher,
  type MatcherDecision,
  type MatcherEnvironment,
} from "@/domain/keybindings/matcher";
import { useVueI18n } from "@/i18n/messages";
import { executeRegisteredCommand, isRegisteredCommandEnabled } from "@/services/command-registry";
import { useKeybindingsStore } from "@/stores/keybindings";
import { handlePluginKeyboardRelease } from "@/services/plugin-keyboard-release";
import { usePluginTakeoverStore } from "@/stores/plugin-takeover";

function hasTextSelection(): boolean {
  const selection = typeof window === "undefined" ? null : window.getSelection();
  return Boolean(selection && selection.rangeCount > 0 && !selection.isCollapsed && selection.toString());
}

function elementTarget(target: EventTarget | null): ElementLike | null {
  if (typeof Element !== "undefined" && target instanceof Element) return target;
  const active = typeof document === "undefined" ? null : document.activeElement;
  return active ?? null;
}

/** Keybinding context for the focused element (used by the palette to filter `when` commands). */
export function currentKeybindingContext(target: EventTarget | null = null): PluginContextValues {
  const store = useKeybindingsStore();
  const element = elementTarget(target);
  return buildKeybindingContext({ target: element, platform: store.platform, textSelection: hasTextSelection() });
}

/** Whether the dispatcher can run a command now (catalog-available app handler or plugin, registered, `when` holds). */
export function canRunKeybindingCommand(command: string, context: PluginContextValues): boolean {
  return canExecute(command, context);
}

function canExecute(command: string, context: PluginContextValues): boolean {
  if (isPluginCommandId(command)) return isRegisteredCommandEnabled(command, context);
  const entry = getKeybindingCommand(command);
  if (!entry || !entry.available || entry.handler !== "app") return false;
  return isRegisteredCommandEnabled(command, context);
}

/** Installs the window listeners; returns the uninstaller. */
export function installKeybindingDispatcher(): () => void {
  const store = useKeybindingsStore();
  const takeover = usePluginTakeoverStore();
  const { t } = useVueI18n();
  const platform = store.platform;
  const matcher = new KeybindingMatcher({
    now: () => performance.now(),
    platform,
    resolution: () => store.resolution,
    doubleModifier: () => store.doubleModifier,
    // Chords typed in Monaco are resolved by Monaco itself; services/monaco-keymap.ts
    // mirrors application bindings into it and runs them through canRunKeybindingCommand.
    monacoMode: () => "skip",
  });
  let hint: MessageHandler | undefined;
  let pendingPrefix: KeyChord[] = [];

  function closeHint() {
    hint?.close();
    hint = undefined;
  }

  function environment(event: Event): MatcherEnvironment {
    const target = elementTarget(event.target);
    const focus = keybindingFocusClass(target);
    const context = buildKeybindingContext({ target, platform, focus, textSelection: hasTextSelection() });
    return { focus, context, canExecute: (command) => canExecute(command, context) };
  }

  // A plugin view that holds the keyboard (features.keyboardCapture) gets every chord.
  function suspended(): boolean {
    return store.recording || takeover.keyboardCaptured();
  }

  function commandTitle(command: string): string {
    const entry = getKeybindingCommand(command);
    return entry ? t(entry.titleKey) : command;
  }

  function run(command: string) {
    closeHint();
    void executeRegisteredCommand(command).catch(() => {
      ElMessage.error(t("commandPalette.actionFailed", { name: commandTitle(command) }));
    });
  }

  function formatChords(chords: readonly KeyChord[]): string {
    return chords.map((chord) => formatKeyChord(chord, platform)).join(" ");
  }

  function apply(event: KeyboardEvent, decision: MatcherDecision) {
    switch (decision.kind) {
      case "ignore":
        return;
      case "consume":
        event.preventDefault();
        return;
      case "execute":
        event.preventDefault();
        event.stopPropagation();
        pendingPrefix = [];
        run(decision.binding.command);
        return;
      case "pending":
        event.preventDefault();
        event.stopPropagation();
        pendingPrefix = decision.prefix;
        closeHint();
        hint = ElMessage({
          type: "info",
          message: t("keybindings.chordPending", { key: formatChords(decision.prefix) }),
          duration: CHORD_TIMEOUT_MS,
        });
        return;
      case "cancel": {
        event.preventDefault();
        event.stopPropagation();
        closeHint();
        if (decision.reason === "mismatch") {
          const chord = keyChordFromEvent(event);
          const key = formatChords(chord ? [...pendingPrefix, chord] : pendingPrefix);
          ElMessage({ type: "warning", message: t("keybindings.chordCancelled", { key }), grouping: true });
        }
        pendingPrefix = [];
        return;
      }
    }
  }

  function onKeydownCapture(event: KeyboardEvent) {
    if (handlePluginKeyboardRelease(event)) return;
    if (suspended()) {
      matcher.cancelPending();
      matcher.resetTaps();
      closeHint();
      return;
    }
    const env = environment(event);
    apply(event, matcher.handleModifierEvent("keydown", event, env));
    if (event.defaultPrevented) return;
    const hadPending = matcher.pending !== null;
    apply(event, matcher.handlePendingKeydown(event, env));
    if (hadPending && matcher.pending === null && !event.defaultPrevented) closeHint();
  }

  function onKeydown(event: KeyboardEvent) {
    if (event.defaultPrevented || suspended()) return;
    apply(event, matcher.handleKeydown(event, environment(event)));
  }

  function onKeyupCapture(event: KeyboardEvent) {
    if (suspended()) return;
    apply(event, matcher.handleModifierEvent("keyup", event, environment(event)));
  }

  function onComposition() {
    matcher.noteComposition();
  }

  function onBlur() {
    matcher.cancelPending();
    matcher.resetTaps();
    pendingPrefix = [];
    closeHint();
  }

  window.addEventListener("keydown", onKeydownCapture, true);
  window.addEventListener("keydown", onKeydown);
  window.addEventListener("keyup", onKeyupCapture, true);
  window.addEventListener("compositionstart", onComposition, true);
  window.addEventListener("compositionend", onComposition, true);
  window.addEventListener("blur", onBlur);
  return () => {
    window.removeEventListener("keydown", onKeydownCapture, true);
    window.removeEventListener("keydown", onKeydown);
    window.removeEventListener("keyup", onKeyupCapture, true);
    window.removeEventListener("compositionstart", onComposition, true);
    window.removeEventListener("compositionend", onComposition, true);
    window.removeEventListener("blur", onBlur);
    closeHint();
  };
}
