import type { KeymapPresetData, KeymapPresetRule } from "@/domain/keybindings/preset";

/**
 * Host panel rules shared by every preset. They are layered after the active
 * preset (and before user overrides), so panel-scoped keys such as ⇧⌘F in the
 * AI panel win over a preset's unscoped binding of the same key.
 */
export const HOST_KEYBINDING_RULES: readonly KeymapPresetRule[] = [
  { key: "mod+f", command: "terminal:find", when: "terminal.focus" },
  // xterm's own ⌘A select-all, kept once the terminal key handler takes ⌘ chords.
  { key: "cmd+a", command: "terminal:select-all", platform: "mac" },
  { key: "escape", command: "ai:stop", when: "ai.panel.focus && ai.running" },
  { key: "mod+shift+o", command: "ai:new-conversation", when: "ai.panel.focus" },
  // `!editor.focus`: ⇧⌘F stays with an editor inside the panel (Eclipse formats on it).
  { key: "mod+shift+f", command: "ai:search-conversations", when: "ai.panel.focus && ai.conversations.global && !editor.focus" },
  { key: "alt+up", command: "ai:previous-conversation", when: "ai.panel.focus && !ai.multiline.field && !editor.focus" },
  { key: "alt+down", command: "ai:next-conversation", when: "ai.panel.focus && !ai.multiline.field && !editor.focus" },
  { key: "mod+c", command: "ai:copy-message", when: "text.selection && ai.message.selection" },
];

const TAB_RULES: KeymapPresetRule[] = Array.from({ length: 9 }, (_, index) => ({
  key: `mod+${index + 1}`,
  command: `workbench:tab-${index + 1}`,
}));

/**
 * MinCode default preset. Lists only application rules: editor commands keep
 * Monaco's built-in (VS Code compatible) defaults from monaco-defaults.ts.
 */
export const MINCODE_PRESET: KeymapPresetData = {
  id: "mincode",
  title: { zh: "MinCode", en: "MinCode" },
  rules: [
    { key: "mod+k", command: "builtin:command-palette" },
    { key: "mod+,", command: "builtin:settings" },
    { key: "mod+i", command: "builtin:ai" },
    // Inside Monaco ⇧⌘L stays "select all occurrences", as before the keymap existed.
    { key: "mod+shift+t", command: "workbench:new-terminal" },
    { key: "mod+w", command: "workbench:close-tab", when: "!terminal.focus" },
    { key: "ctrl+tab", command: "workbench:next-tab" },
    { key: "ctrl+shift+tab", command: "workbench:prev-tab" },
    ...TAB_RULES,
    { key: "mod+s", command: "editor:save", when: 'editor.focus && editor.kind != "sql"' },
    { key: "mod+s", command: "database:run-query", when: 'editor.focus && editor.kind == "sql"' },
    { key: "mod+enter", command: "database:run-query", when: 'editor.focus && editor.kind == "sql"' },
    { key: "mod+s", command: "database:submit-grid-changes", when: "!editor.focus && !dialog.focus" },
    { key: "alt+down", command: "editor:mincode.diff.next", when: 'editor.kind == "diff"' },
    { key: "alt+up", command: "editor:mincode.diff.previous", when: 'editor.kind == "diff"' },
    { key: "cmd+c", command: "terminal:copy", when: "terminal.selection", platform: "mac" },
    { key: "ctrl+shift+c", command: "terminal:copy", platform: ["win", "linux"] },
    { key: "ctrl+c", command: "terminal:copy", when: "terminal.selection", platform: ["win", "linux"] },
    { key: "cmd+v", command: "terminal:paste", platform: "mac" },
    { key: "ctrl+shift+v", command: "terminal:paste", platform: ["win", "linux"] },
    { key: "ctrl+v", command: "terminal:paste", platform: ["win", "linux"] },
    // Terminal tabs (inside the terminal workspace view). Ctrl+Alt+T opens a system terminal
    // on Linux (reserved), so Linux has no default key for a new terminal tab.
    { key: "cmd+t", command: "terminal:new-tab", when: "terminal.focus", platform: "mac" },
    { key: "ctrl+alt+t", command: "terminal:new-tab", when: "terminal.focus", platform: "win" },
    { key: "cmd+shift+[", command: "terminal:prev-tab", when: "terminal.focus", platform: "mac" },
    { key: "ctrl+pageup", command: "terminal:prev-tab", when: "terminal.focus", platform: ["win", "linux"] },
    { key: "cmd+shift+]", command: "terminal:next-tab", when: "terminal.focus", platform: "mac" },
    { key: "ctrl+pagedown", command: "terminal:next-tab", when: "terminal.focus", platform: ["win", "linux"] },
  ],
};
