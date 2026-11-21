/**
 * Import / export of user keybindings in the VS Code keybindings.json shape
 * ([{ key, command, when? }], JSONC). Import maps VS Code command ids and
 * when-keys onto MinCode ids through alias tables and reports every entry it
 * cannot represent instead of guessing. Export always writes MinCode ids.
 */
import { parseWhenExpression } from "@/domain/commands/when";
import { isBindableCommandId } from "@/domain/keybindings/catalog";
import { parseKeySequence, type KeyboardPlatform } from "@/domain/keybindings/keys";
import type { KeybindingRule } from "@/domain/keybindings/preset";

export const KEYBINDING_IMPORT_MAX_ENTRIES = 500;
export const KEYBINDING_IMPORT_MAX_BYTES = 256 * 1024;

export const VSCODE_COMMAND_ALIASES: Readonly<Record<string, string>> = Object.freeze({
  "workbench.action.showCommands": "builtin:command-palette",
  "workbench.action.quickOpen": "builtin:quick-open",
  "workbench.action.findInFiles": "builtin:find-in-files",
  "workbench.action.openRecent": "builtin:recent-files",
  "workbench.action.showAllSymbols": "builtin:go-to-symbol",
  "workbench.action.openSettings": "builtin:settings",
  "workbench.action.openGlobalKeybindings": "builtin:keybindings",
  "workbench.action.toggleSidebarVisibility": "workbench:toggle-sidebar",
  "workbench.action.terminal.toggleTerminal": "workbench:show-view.terminal",
  "workbench.action.terminal.new": "workbench:new-terminal",
  "workbench.view.extensions": "workbench:show-view.plugins",
  "workbench.action.closeActiveEditor": "workbench:close-tab",
  "workbench.action.reopenClosedEditor": "workbench:reopen-closed",
  "workbench.action.nextEditor": "workbench:next-tab",
  "workbench.action.previousEditor": "workbench:prev-tab",
  "workbench.action.navigateBack": "workbench:navigate-back",
  "workbench.action.navigateForward": "workbench:navigate-forward",
  "workbench.action.files.save": "editor:save",
  "workbench.action.terminal.copySelection": "terminal:copy",
  "workbench.action.terminal.paste": "terminal:paste",
  "workbench.action.terminal.focusFind": "terminal:find",
  "workbench.action.terminal.clear": "terminal:clear",
  ...Object.fromEntries(Array.from({ length: 9 }, (_, index) => [`workbench.action.openEditorAtIndex${index + 1}`, `workbench:tab-${index + 1}`])),
});

/** VS Code when-keys with a MinCode equivalent (whole clause replacement). */
export const VSCODE_WHEN_ALIASES: Readonly<Record<string, string>> = Object.freeze({
  editorTextFocus: "editor.focus",
  editorFocus: "editor.focus",
  textInputFocus: "editor.focus",
  terminalFocus: "terminal.focus",
  inputFocus: "input.focus",
  isInDiffEditor: 'editor.kind == "diff"',
  terminalTextSelected: "terminal.selection",
});

export interface KeybindingImportIssue {
  index: number;
  entry: unknown;
  reason: "shape" | "command" | "key" | "when";
  detail: string;
}

export interface KeybindingImportResult {
  rules: KeybindingRule[];
  issues: KeybindingImportIssue[];
}

/** Strips // and block comments plus trailing commas, preserving string contents. */
export function stripJsonc(text: string): string {
  let result = "";
  let index = 0;
  while (index < text.length) {
    const char = text[index]!;
    if (char === '"') {
      let end = index + 1;
      while (end < text.length && text[end] !== '"') end += text[end] === "\\" ? 2 : 1;
      result += text.slice(index, end + 1);
      index = end + 1;
    } else if (char === "/" && text[index + 1] === "/") {
      while (index < text.length && text[index] !== "\n") index++;
    } else if (char === "/" && text[index + 1] === "*") {
      const end = text.indexOf("*/", index + 2);
      index = end < 0 ? text.length : end + 2;
    } else {
      result += char;
      index++;
    }
  }
  return result.replace(/,(\s*[\]}])/g, "$1");
}

function mapCommand(command: string): string | null {
  const removal = command.startsWith("-");
  const id = removal ? command.slice(1) : command;
  let mapped: string | undefined;
  if (isBindableCommandId(id)) mapped = id;
  else if (VSCODE_COMMAND_ALIASES[id]) mapped = VSCODE_COMMAND_ALIASES[id];
  else if (isBindableCommandId(`editor:${id}`)) mapped = `editor:${id}`;
  if (!mapped) return null;
  return removal ? `-${mapped}` : mapped;
}

/** Maps a VS Code when expression to the MinCode dialect, or explains why it cannot. */
export function mapWhenExpression(when: string): { ok: true; when: string } | { ok: false; detail: string } {
  const trimmed = when.trim();
  if (!trimmed) return { ok: true, when: "" };
  if (/\|\||=~|[()]|\bin\b|\bnot in\b|[<>]/.test(trimmed)) {
    return { ok: false, detail: "unsupported operator (||, =~, parentheses, in, comparisons)" };
  }
  const clauses = trimmed.split("&&").map((part) => part.trim());
  const mapped: string[] = [];
  for (const clause of clauses) {
    const negate = clause.startsWith("!");
    const body = negate ? clause.slice(1).trim() : clause;
    const alias = VSCODE_WHEN_ALIASES[body];
    if (alias) {
      if (negate && alias.includes("==")) mapped.push(alias.replace("==", "!="));
      else mapped.push(`${negate ? "!" : ""}${alias}`);
      continue;
    }
    // Already MinCode dialect (lowercase dotted key, optional quoted comparison).
    const normalized = clause.replace(/'([^']*)'/g, '"$1"');
    try {
      parseWhenExpression(normalized);
      mapped.push(normalized);
    } catch {
      return { ok: false, detail: `unsupported clause ${JSON.stringify(clause)}` };
    }
  }
  const expression = mapped.join(" && ");
  try {
    parseWhenExpression(expression);
  } catch (error) {
    return { ok: false, detail: error instanceof Error ? error.message : String(error) };
  }
  return { ok: true, when: expression };
}

/**
 * Parses a keybindings.json document. Keys are validated for the current
 * platform (VS Code files are per-platform too); entries that cannot be
 * represented are reported and skipped.
 */
export function importKeybindings(text: string, platform: KeyboardPlatform): KeybindingImportResult {
  if (new TextEncoder().encode(text).length > KEYBINDING_IMPORT_MAX_BYTES) {
    throw new Error(`keybindings file exceeds ${KEYBINDING_IMPORT_MAX_BYTES} bytes`);
  }
  const parsed: unknown = JSON.parse(stripJsonc(text));
  if (!Array.isArray(parsed)) throw new Error("keybindings file must be a JSON array");
  if (parsed.length > KEYBINDING_IMPORT_MAX_ENTRIES) {
    throw new Error(`keybindings file has more than ${KEYBINDING_IMPORT_MAX_ENTRIES} entries`);
  }
  const rules: KeybindingRule[] = [];
  const issues: KeybindingImportIssue[] = [];
  parsed.forEach((entry: unknown, index) => {
    const issue = (reason: KeybindingImportIssue["reason"], detail: string) => issues.push({ index, entry, reason, detail });
    if (typeof entry !== "object" || entry === null || Array.isArray(entry)) return issue("shape", "entry must be an object");
    const record = entry as Record<string, unknown>;
    if (typeof record.command !== "string" || (record.key !== undefined && typeof record.key !== "string")) {
      return issue("shape", "entry needs a string command and key");
    }
    if (record.when !== undefined && typeof record.when !== "string") return issue("shape", "when must be a string");
    const command = mapCommand(record.command);
    if (!command) return issue("command", record.command);
    const removal = command.startsWith("-");
    if (record.key === undefined && !removal) return issue("key", "missing key");
    if (typeof record.key === "string" && !parseKeySequence(record.key, platform)) return issue("key", record.key);
    const rule: KeybindingRule = { command };
    if (typeof record.key === "string") rule.key = record.key.trim();
    if (typeof record.when === "string" && record.when.trim()) {
      const when = mapWhenExpression(record.when);
      if (!when.ok) return issue("when", when.detail);
      if (when.when) rule.when = when.when;
    }
    rules.push(rule);
  });
  return { rules, issues };
}

/** Serializes user rules as a keybindings.json document with MinCode ids. */
export function exportKeybindings(rules: readonly KeybindingRule[]): string {
  const entries = rules.map((rule) => {
    const entry: Record<string, string> = {};
    if (rule.key !== undefined) entry.key = rule.key;
    entry.command = rule.command;
    if (rule.when) entry.when = rule.when;
    return entry;
  });
  return `${JSON.stringify(entries, null, 2)}\n`;
}
