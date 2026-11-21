/**
 * Curated Monaco default keybindings (the subset MinCode's presets can
 * collide with). Monaco registers these itself; the table lets the resolver
 * show them as "default" bindings and lets monaco-rules generate `-command`
 * removal rules when a preset re-binds a command or takes over its key.
 *
 * scripts/keybinding-presets.test.mjs checks every id here still exists in the
 * Monaco sources MinCode imports, so a Monaco upgrade that renames an action
 * fails the test instead of silently leaving a stale binding.
 */
import type { KeyboardPlatform } from "@/domain/keybindings/keys";
import { catalogCommandForMonaco } from "@/domain/keybindings/catalog";
import type { KeybindingRule } from "@/domain/keybindings/preset";

export interface MonacoDefaultBinding {
  /** Monaco command id. */
  command: string;
  key: string;
  platform?: KeyboardPlatform | KeyboardPlatform[];
  /** Monaco's own `when` (Monaco context keys), kept for removal rules. */
  monacoWhen?: string;
}

const MAC = "mac" as const;
const PC: KeyboardPlatform[] = ["win", "linux"];
const MAC_WIN: KeyboardPlatform[] = ["mac", "win"];
const TEXT = "editorTextFocus";
const WRITABLE = "editorTextFocus && !editorReadonly";

export const MONACO_DEFAULT_BINDINGS: readonly MonacoDefaultBinding[] = Object.freeze([
  { command: "editor.action.copyLinesUpAction", key: "shift+alt+up", platform: MAC_WIN, monacoWhen: WRITABLE },
  { command: "editor.action.copyLinesUpAction", key: "ctrl+shift+alt+up", platform: "linux", monacoWhen: WRITABLE },
  { command: "editor.action.copyLinesDownAction", key: "shift+alt+down", platform: MAC_WIN, monacoWhen: WRITABLE },
  { command: "editor.action.copyLinesDownAction", key: "ctrl+shift+alt+down", platform: "linux", monacoWhen: WRITABLE },
  { command: "editor.action.moveLinesUpAction", key: "alt+up", monacoWhen: WRITABLE },
  { command: "editor.action.moveLinesDownAction", key: "alt+down", monacoWhen: WRITABLE },
  { command: "editor.action.deleteLines", key: "mod+shift+k", monacoWhen: WRITABLE },
  { command: "editor.action.commentLine", key: "mod+/", monacoWhen: WRITABLE },
  { command: "editor.action.addCommentLine", key: "mod+k mod+c", monacoWhen: WRITABLE },
  { command: "editor.action.removeCommentLine", key: "mod+k mod+u", monacoWhen: WRITABLE },
  { command: "editor.action.blockComment", key: "shift+alt+a", platform: MAC_WIN, monacoWhen: WRITABLE },
  { command: "editor.action.blockComment", key: "ctrl+shift+a", platform: "linux", monacoWhen: WRITABLE },
  { command: "editor.action.formatDocument", key: "shift+alt+f", platform: MAC_WIN, monacoWhen: WRITABLE },
  { command: "editor.action.formatDocument", key: "ctrl+shift+i", platform: "linux", monacoWhen: WRITABLE },
  { command: "editor.action.formatSelection", key: "mod+k mod+f", monacoWhen: WRITABLE },
  { command: "editor.action.trimTrailingWhitespace", key: "mod+k mod+x", monacoWhen: WRITABLE },
  { command: "editor.action.rename", key: "f2", monacoWhen: WRITABLE },
  { command: "editor.action.revealDefinition", key: "f12", monacoWhen: TEXT },
  { command: "editor.action.goToImplementation", key: "mod+f12", monacoWhen: TEXT },
  { command: "editor.action.peekImplementation", key: "mod+shift+f12", monacoWhen: TEXT },
  { command: "editor.action.goToReferences", key: "shift+f12", monacoWhen: TEXT },
  { command: "editor.action.addSelectionToNextFindMatch", key: "mod+d", monacoWhen: "editorFocus" },
  { command: "editor.action.moveSelectionToNextFindMatch", key: "mod+k mod+d", monacoWhen: "editorFocus" },
  { command: "editor.action.selectHighlights", key: "mod+shift+l", monacoWhen: "editorFocus" },
  { command: "editor.action.changeAll", key: "mod+f2", monacoWhen: WRITABLE },
  { command: "editor.action.insertCursorAbove", key: "alt+cmd+up", platform: MAC, monacoWhen: TEXT },
  { command: "editor.action.insertCursorBelow", key: "alt+cmd+down", platform: MAC, monacoWhen: TEXT },
  { command: "editor.action.insertCursorAbove", key: "ctrl+alt+up", platform: "win", monacoWhen: TEXT },
  { command: "editor.action.insertCursorBelow", key: "ctrl+alt+down", platform: "win", monacoWhen: TEXT },
  { command: "editor.action.insertCursorAbove", key: "shift+alt+up", platform: "linux", monacoWhen: TEXT },
  { command: "editor.action.insertCursorBelow", key: "shift+alt+down", platform: "linux", monacoWhen: TEXT },
  { command: "editor.action.insertCursorAbove", key: "ctrl+shift+up", platform: "linux", monacoWhen: TEXT },
  { command: "editor.action.insertCursorBelow", key: "ctrl+shift+down", platform: "linux", monacoWhen: TEXT },
  { command: "editor.action.insertCursorAtEndOfEachLineSelected", key: "shift+alt+i", monacoWhen: TEXT },
  { command: "editor.action.smartSelect.expand", key: "ctrl+shift+cmd+right", platform: MAC, monacoWhen: TEXT },
  { command: "editor.action.smartSelect.shrink", key: "ctrl+shift+cmd+left", platform: MAC, monacoWhen: TEXT },
  { command: "editor.action.smartSelect.expand", key: "ctrl+shift+right", platform: MAC, monacoWhen: TEXT },
  { command: "editor.action.smartSelect.shrink", key: "ctrl+shift+left", platform: MAC, monacoWhen: TEXT },
  { command: "editor.action.smartSelect.expand", key: "shift+alt+right", platform: PC, monacoWhen: TEXT },
  { command: "editor.action.smartSelect.shrink", key: "shift+alt+left", platform: PC, monacoWhen: TEXT },
  { command: "editor.action.quickFix", key: "mod+.", monacoWhen: WRITABLE },
  { command: "editor.action.triggerParameterHints", key: "mod+shift+space", monacoWhen: WRITABLE },
  { command: "editor.action.gotoLine", key: "ctrl+g", monacoWhen: "editorFocus" },
  { command: "actions.find", key: "mod+f", monacoWhen: "editorFocus || editorIsOpen" },
  { command: "editor.action.startFindReplaceAction", key: "ctrl+h", platform: PC, monacoWhen: "editorFocus || editorIsOpen" },
  { command: "editor.action.startFindReplaceAction", key: "alt+cmd+f", platform: MAC, monacoWhen: "editorFocus || editorIsOpen" },
  { command: "editor.action.nextMatchFindAction", key: "f3", monacoWhen: "editorFocus" },
  { command: "editor.action.nextMatchFindAction", key: "cmd+g", platform: MAC, monacoWhen: "editorFocus" },
  { command: "editor.action.marker.next", key: "alt+f8", monacoWhen: "editorFocus" },
  { command: "editor.action.marker.prev", key: "shift+alt+f8", monacoWhen: "editorFocus" },
  { command: "editor.action.marker.nextInFiles", key: "f8", monacoWhen: "editorFocus" },
  { command: "editor.action.marker.prevInFiles", key: "shift+f8", monacoWhen: "editorFocus" },
  { command: "editor.action.joinLines", key: "ctrl+j", platform: MAC, monacoWhen: WRITABLE },
  { command: "deleteAllLeft", key: "cmd+backspace", platform: MAC, monacoWhen: WRITABLE },
  { command: "deleteAllRight", key: "ctrl+k", platform: MAC, monacoWhen: WRITABLE },
  { command: "deleteAllRight", key: "cmd+delete", platform: MAC, monacoWhen: WRITABLE },
  { command: "redo", key: "ctrl+y", platform: PC, monacoWhen: TEXT },
  { command: "redo", key: "ctrl+shift+z", platform: PC, monacoWhen: TEXT },
  { command: "redo", key: "shift+cmd+z", platform: MAC, monacoWhen: TEXT },
  { command: "expandLineSelection", key: "mod+l", monacoWhen: TEXT },
  { command: "cursorTopSelect", key: "ctrl+shift+home", platform: PC, monacoWhen: TEXT },
  { command: "cursorTopSelect", key: "shift+cmd+up", platform: MAC, monacoWhen: TEXT },
  { command: "cursorBottomSelect", key: "ctrl+shift+end", platform: PC, monacoWhen: TEXT },
  { command: "cursorBottomSelect", key: "shift+cmd+down", platform: MAC, monacoWhen: TEXT },
  { command: "editor.action.insertLineAfter", key: "mod+enter", monacoWhen: WRITABLE },
  { command: "editor.action.insertLineBefore", key: "mod+shift+enter", monacoWhen: WRITABLE },
]);

function appliesTo(binding: MonacoDefaultBinding, platform: KeyboardPlatform): boolean {
  if (binding.platform === undefined) return true;
  return Array.isArray(binding.platform) ? binding.platform.includes(platform) : binding.platform === platform;
}

/** Monaco default bindings effective on one platform. */
export function monacoDefaultBindings(platform: KeyboardPlatform): MonacoDefaultBinding[] {
  return MONACO_DEFAULT_BINDINGS.filter((binding) => appliesTo(binding, platform));
}

/**
 * The resolver's "default" layer: Monaco defaults of catalog editor commands,
 * expressed as MinCode rules (`editor:<monaco id>`). The resolver adds the
 * implicit `editor.focus` for monaco-handled commands.
 */
export function monacoDefaultRules(platform: KeyboardPlatform): KeybindingRule[] {
  const rules: KeybindingRule[] = [];
  for (const binding of monacoDefaultBindings(platform)) {
    const command = catalogCommandForMonaco(binding.command);
    if (command) rules.push({ key: binding.key, command: command.id });
  }
  return rules;
}
