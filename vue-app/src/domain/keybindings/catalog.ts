/**
 * Static catalog of every command a keybinding can target. The resolver,
 * settings UI and preset validator all read this list, so a command is known
 * (and bindable) even before its handler is registered. Handlers are attached
 * at runtime through services/command-registry.ts (`handler: "app"`), Monaco
 * (`handler: "monaco"`, phase 3 mirrors rules into Monaco) or the terminal key
 * handler (`handler: "terminal"`, phase 4). `available: false` marks commands
 * that are designed but have no handler yet (the dispatcher leaves their keys
 * untouched).
 *
 * Plugin commands (`plugin:<pluginId>:<commandId>`) are not listed here; they
 * are always bindable and resolved lazily at execution time.
 */
import type { MessageKey } from "@/i18n/locales/zh";

export type KeybindingCategory =
  | "general"
  | "navigation"
  | "view"
  | "editor"
  | "terminal"
  | "ai"
  | "project"
  | "database";

export type KeybindingScope = "global" | "editor" | "diff" | "terminal" | "ai" | "project" | "database";

export type KeybindingHandlerKind = "app" | "monaco" | "terminal";

export interface KeybindingCommand {
  id: string;
  titleKey: MessageKey;
  category: KeybindingCategory;
  scope: KeybindingScope;
  handler: KeybindingHandlerKind;
  /** Monaco command id executed for `handler: "monaco"`. */
  monacoCommand?: string;
  /** Whether the registered handler is listed in the command palette. */
  showInPalette: boolean;
  /** A handler exists today. */
  available: boolean;
  /** The binding may fire while a text field is focused (panel keys such as Esc, ⌥↑/↓). */
  allowInInput?: boolean;
  /** Fire on auto-repeat keydown events (default: ignore repeats). */
  repeatable?: boolean;
}

export const PLUGIN_COMMAND_PREFIX = "plugin:";

/**
 * A keymap preset rule's `command` as the plugin host accepts it (the same
 * expression as backend/plugin/keymaps.go `keymapCommandPattern`;
 * scripts/keybinding-presets.test.mjs keeps them equal): an optional `-`
 * (removal), a namespace, `:`, then the name. Every catalog id must match, or
 * a preset binding it fails the host's install validation.
 */
export const KEYMAP_COMMAND_PATTERN = new RegExp("^-?(builtin|workbench|editor|terminal|project|ai|database|plugin):[A-Za-z0-9][A-Za-z0-9._:\\-]*$");
/** backend/plugin/keymaps.go `keymapMaxCommandLength` (contract `keymaps.limits.commandMaxLength`). */
export const KEYMAP_COMMAND_MAX_LENGTH = 200;

type Entry = Omit<KeybindingCommand, "showInPalette" | "available"> &
  Partial<Pick<KeybindingCommand, "showInPalette" | "available">>;

function app(
  id: string,
  titleKey: MessageKey,
  category: KeybindingCategory,
  options: Partial<Entry> = {},
): Entry {
  return { id, titleKey, category, scope: "global", handler: "app", ...options };
}

function editor(monacoCommand: string, titleKey: MessageKey, options: Partial<Entry> = {}): Entry {
  return {
    id: `editor:${monacoCommand}`,
    titleKey,
    category: "editor",
    scope: "editor",
    handler: "monaco",
    monacoCommand,
    showInPalette: false,
    available: true,
    ...options,
  };
}

const ENTRIES: Entry[] = [
  // General
  app("builtin:command-palette", "keybindings.command.builtin:command-palette", "general", { showInPalette: false, available: true }),
  app("builtin:quick-open", "keybindings.command.builtin:quick-open", "navigation", { available: true }),
  app("builtin:search-everywhere", "keybindings.command.builtin:search-everywhere", "navigation", { available: true }),
  app("builtin:find-in-files", "keybindings.command.builtin:find-in-files", "navigation", { available: true }),
  app("builtin:recent-files", "keybindings.command.builtin:recent-files", "navigation", { available: true }),
  // No workspace-symbol provider behind the palette yet (LSP workspace/symbol is not wired).
  app("builtin:go-to-symbol", "keybindings.command.builtin:go-to-symbol", "navigation"),
  app("builtin:settings", "keybindings.command.builtin:settings", "general", { showInPalette: true, available: true }),
  app("builtin:keybindings", "keybindings.command.builtin:keybindings", "general", { showInPalette: true, available: true }),
  app("builtin:ai", "keybindings.command.builtin:ai", "general", { showInPalette: true, available: true }),
  // Workbench
  // Registered by the active view (project, database, HTTP, SSH, sessions) while it is shown.
  app("workbench:toggle-sidebar", "keybindings.command.workbench:toggle-sidebar", "view", { showInPalette: true, available: true }),
  app("workbench:show-view.terminal", "keybindings.command.workbench:show-view.terminal", "view", { available: true }),
  app("workbench:new-terminal", "keybindings.command.workbench:new-terminal", "view", { showInPalette: true, available: true }),
  // Registered by the terminal workspace view while it is shown (tabs inside a terminal workbench tab).
  app("terminal:new-tab", "keybindings.command.terminal:new-tab", "view", { showInPalette: true, available: true }),
  app("terminal:next-tab", "keybindings.command.terminal:next-tab", "view", { repeatable: true, available: true }),
  app("terminal:prev-tab", "keybindings.command.terminal:prev-tab", "view", { repeatable: true, available: true }),
  app("workbench:close-tab", "keybindings.command.workbench:close-tab", "navigation", { showInPalette: true, available: true }),
  app("workbench:close-all", "keybindings.command.workbench:close-all", "navigation", { showInPalette: true, available: true }),
  app("workbench:reopen-closed", "keybindings.command.workbench:reopen-closed", "navigation", { showInPalette: true, available: true }),
  app("workbench:next-tab", "keybindings.command.workbench:next-tab", "navigation", { repeatable: true, available: true }),
  app("workbench:prev-tab", "keybindings.command.workbench:prev-tab", "navigation", { repeatable: true, available: true }),
  ...Array.from({ length: 9 }, (_, index) =>
    app(`workbench:tab-${index + 1}`, `keybindings.command.workbench:tab-${index + 1}` as MessageKey, "navigation", { available: true }),
  ),
  // No editor navigation history exists yet.
  app("workbench:navigate-back", "keybindings.command.workbench:navigate-back", "navigation"),
  app("workbench:navigate-forward", "keybindings.command.workbench:navigate-forward", "navigation"),
  // Editor host commands (per-editor handlers; phase 3 dispatches to the focused editor)
  { ...app("editor:save", "keybindings.command.editor:save", "editor", { available: true }), scope: "editor" },
  // Registered by the project workspace; elsewhere Monaco falls back to saving the focused editor.
  { ...app("editor:save-all", "keybindings.command.editor:save-all", "editor", { showInPalette: true, available: true }), scope: "editor" },
  { ...app("database:run-query", "keybindings.command.database:run-query", "database", { available: true }), scope: "database" },
  // Registered by the database workspace while it is shown: submits the result grid's staged changes.
  {
    ...app("database:submit-grid-changes", "keybindings.command.database:submit-grid-changes", "database", { available: true, allowInInput: true }),
    scope: "database",
  },
  // Monaco editor commands
  editor("editor.action.copyLinesUpAction", "keybindings.command.editor:editor.action.copyLinesUpAction"),
  editor("editor.action.copyLinesDownAction", "keybindings.command.editor:editor.action.copyLinesDownAction"),
  editor("editor.action.duplicateSelection", "keybindings.command.editor:editor.action.duplicateSelection"),
  editor("editor.action.deleteLines", "keybindings.command.editor:editor.action.deleteLines"),
  editor("editor.action.commentLine", "keybindings.command.editor:editor.action.commentLine"),
  editor("editor.action.blockComment", "keybindings.command.editor:editor.action.blockComment"),
  editor("editor.action.formatDocument", "keybindings.command.editor:editor.action.formatDocument"),
  editor("editor.action.rename", "keybindings.command.editor:editor.action.rename"),
  editor("editor.action.revealDefinition", "keybindings.command.editor:editor.action.revealDefinition"),
  editor("editor.action.goToImplementation", "keybindings.command.editor:editor.action.goToImplementation"),
  editor("editor.action.goToReferences", "keybindings.command.editor:editor.action.goToReferences"),
  editor("editor.action.addSelectionToNextFindMatch", "keybindings.command.editor:editor.action.addSelectionToNextFindMatch"),
  editor("editor.action.selectHighlights", "keybindings.command.editor:editor.action.selectHighlights"),
  editor("editor.action.insertCursorAbove", "keybindings.command.editor:editor.action.insertCursorAbove"),
  editor("editor.action.insertCursorBelow", "keybindings.command.editor:editor.action.insertCursorBelow"),
  editor("editor.action.moveLinesUpAction", "keybindings.command.editor:editor.action.moveLinesUpAction"),
  editor("editor.action.moveLinesDownAction", "keybindings.command.editor:editor.action.moveLinesDownAction"),
  editor("editor.action.smartSelect.expand", "keybindings.command.editor:editor.action.smartSelect.expand"),
  editor("editor.action.smartSelect.shrink", "keybindings.command.editor:editor.action.smartSelect.shrink"),
  editor("editor.action.quickFix", "keybindings.command.editor:editor.action.quickFix"),
  editor("editor.action.triggerParameterHints", "keybindings.command.editor:editor.action.triggerParameterHints"),
  editor("editor.action.triggerSuggest", "keybindings.command.editor:editor.action.triggerSuggest"),
  editor("editor.action.gotoLine", "keybindings.command.editor:editor.action.gotoLine"),
  editor("actions.find", "keybindings.command.editor:actions.find"),
  editor("editor.action.startFindReplaceAction", "keybindings.command.editor:editor.action.startFindReplaceAction"),
  editor("editor.action.marker.nextInFiles", "keybindings.command.editor:editor.action.marker.nextInFiles"),
  editor("editor.action.marker.prevInFiles", "keybindings.command.editor:editor.action.marker.prevInFiles"),
  editor("editor.action.joinLines", "keybindings.command.editor:editor.action.joinLines"),
  editor("mincode.diff.next", "keybindings.command.editor:mincode.diff.next", { scope: "diff" }),
  editor("mincode.diff.previous", "keybindings.command.editor:mincode.diff.previous", { scope: "diff" }),
  // Terminal (run by the xterm key handler, composables/use-terminal-keybindings.ts)
  ...(["copy", "paste", "find", "clear", "select-all"] as const).map(
    (name): Entry => ({
      ...app(`terminal:${name}`, `keybindings.command.terminal:${name}`, "terminal", { available: true }),
      scope: "terminal",
      handler: "terminal",
    }),
  ),
  // AI assistant panel
  ...(["new-conversation", "search-conversations", "previous-conversation", "next-conversation", "stop"] as const).map(
    (name): Entry => ({
      ...app(`ai:${name}`, `keybindings.command.ai:${name}`, "ai", { available: true, allowInInput: true }),
      scope: "ai",
      repeatable: name === "previous-conversation" || name === "next-conversation",
    }),
  ),
  { ...app("ai:copy-message", "keybindings.command.ai:copy-message", "ai", { available: true }), scope: "ai" },
  // Project explorer
  { ...app("project:copy-node", "keybindings.command.project:copy-node", "project", { available: true }), scope: "project" },
  { ...app("project:paste-node", "keybindings.command.project:paste-node", "project", { available: true }), scope: "project" },
  // Debugger (registered by the project workspace while it is shown, composables/use-debug-commands.ts).
  ...(
    [
      "stepOver",
      "stepInto",
      "smartStepInto",
      "stepOut",
      "runToCursor",
      "resume",
      "evaluate",
      "toggleBreakpoint",
      "run",
      "debug",
      "stop",
    ] as const
  ).map(
    (name): Entry => ({
      ...app(`project:debug.${name}`, `debug.command.${name}`, "project", { showInPalette: true, available: true }),
      scope: "project",
      repeatable: name.startsWith("step"),
    }),
  ),
];

export const KEYBINDING_COMMANDS: readonly KeybindingCommand[] = Object.freeze(
  ENTRIES.map((entry) => Object.freeze({ showInPalette: false, available: false, ...entry })),
);

const BY_ID = new Map(KEYBINDING_COMMANDS.map((command) => [command.id, command]));

export function getKeybindingCommand(id: string): KeybindingCommand | undefined {
  return BY_ID.get(id);
}

export function isPluginCommandId(id: string): boolean {
  return id.startsWith(PLUGIN_COMMAND_PREFIX) && id.length > PLUGIN_COMMAND_PREFIX.length && id.length <= 200;
}

/** A command id that a keybinding may target: a catalog command or a plugin command. */
export function isBindableCommandId(id: string): boolean {
  return BY_ID.has(id) || isPluginCommandId(id);
}

/** Maps a Monaco command id to its catalog entry (`editor.action.rename` → `editor:editor.action.rename`). */
export function catalogCommandForMonaco(monacoCommand: string): KeybindingCommand | undefined {
  return BY_ID.get(`editor:${monacoCommand}`);
}
