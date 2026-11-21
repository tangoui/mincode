/**
 * Applies the active keymap to Monaco. Monaco's keybinding service is a
 * process-wide singleton, so there is exactly one rule set:
 * `ensureMonacoKeymap()` (called from every Monaco host's `onMounted`)
 * subscribes once to the keybindings store and, on every change, disposes the
 * previous `monaco.editor.addKeybindingRules` set and adds the one built by
 * domain/keybindings/monaco-keymap-rules.ts. Preset switches therefore apply
 * live, including the key labels in Monaco's context menu.
 *
 * It also owns the global Monaco commands those rules target:
 *  - `mincode.app.run` runs a MinCode command (`commandArgs.id`) after
 *    checking its when clause against the current MinCode context;
 *  - `mincode.editor.save` / `mincode.editor.run` call the handlers of the
 *    focused editor registered through `registerMonacoEditor`;
 *  - `mincode.diff.next` / `mincode.diff.previous` navigate the focused diff
 *    editor registered through `registerMonacoDiffNavigation`.
 *
 * Editor modes (Vim): when the active preset requests `editorMode` and a
 * provider is registered, rules on the sequences it owns are left out and
 * the provider is attached to every registered editor.
 */
import { effectScope, watch } from "vue";
import { ElMessage } from "element-plus";
import * as monaco from "@/services/monaco-runtime.js";
import { evaluateWhenClauses, parseWhenExpression } from "@/domain/commands/when";
import { getKeybindingCommand } from "@/domain/keybindings/catalog";
import { registerKeybindingContext } from "@/domain/keybindings/context";
import type { EditorModeDisposable, EditorModeProvider } from "@/domain/keybindings/editor-mode";
import type { KeySequence } from "@/domain/keybindings/keys";
import {
  buildMonacoKeymapRules,
  MONACO_APP_COMMAND,
  MONACO_EDITOR_KIND_CONTEXT_KEY,
  MONACO_EDITOR_RUN_COMMAND,
  MONACO_EDITOR_SAVE_COMMAND,
  type MonacoCommandArgs,
  type MonacoKeymapResult,
} from "@/domain/keybindings/monaco-keymap-rules";
import { useVueI18n } from "@/i18n/messages";
import { executeRegisteredCommand } from "@/services/command-registry";
import { canRunKeybindingCommand, currentKeybindingContext } from "@/services/keybinding-dispatcher";
import { useKeybindingsStore } from "@/stores/keybindings";
import { registerVimEditorMode } from "@/services/vim-editor-mode";
import { ensureMonacoContributions } from "@/services/monaco-contributions";
import { attachMonacoMenuContext, type MonacoMenuContext } from "@/services/monaco-menus";

/** `editor.kind` of a MinCode text editor (the Monaco context key `mincodeEditorKind`). */
export type MonacoEditorKind = "text" | "project" | "sql" | "sftp" | "json" | "conflict" | "ddl";

export interface MonacoEditorHandlers {
  /** `editor:save` while this editor has focus. */
  save?: () => void;
  /** `database:run-query` while this editor has focus. */
  run?: () => void;
}

export interface MonacoEditorRegistration {
  setKind(kind: MonacoEditorKind): void;
  dispose(): void;
}

interface EditorEntry {
  editor: monaco.editor.IStandaloneCodeEditor;
  handlers: MonacoEditorHandlers;
}

interface DiffEntry {
  diffEditor: monaco.editor.IStandaloneDiffEditor;
  navigate: (direction: "next" | "previous") => void;
}

const editors = new Set<EditorEntry>();
const diffEditors = new Set<DiffEntry>();
let installed = false;
let appliedRules: monaco.IDisposable | null = null;
let lastResult: MonacoKeymapResult | null = null;

function isInside(container: HTMLElement, target: unknown): boolean {
  return typeof Node !== "undefined" && target instanceof Node && container.contains(target);
}

function focusedEditor(): EditorEntry | undefined {
  for (const entry of editors) if (entry.editor.hasWidgetFocus()) return entry;
  return undefined;
}

function focusedDiffEditor(): DiffEntry | undefined {
  for (const entry of diffEditors) {
    if (entry.diffEditor.getOriginalEditor().hasWidgetFocus() || entry.diffEditor.getModifiedEditor().hasWidgetFocus()) {
      return entry;
    }
  }
  return undefined;
}

function commandArgs(value: unknown): MonacoCommandArgs | null {
  if (!value || typeof value !== "object") return null;
  const { id, when, fallback } = value as Record<string, unknown>;
  if (typeof id !== "string") return null;
  return {
    id,
    when: typeof when === "string" ? when : undefined,
    fallback: typeof fallback === "string" ? fallback : undefined,
  };
}

function runtimeWhenHolds(args: MonacoCommandArgs): boolean {
  if (!args.when) return true;
  try {
    return evaluateWhenClauses(parseWhenExpression(args.when), currentKeybindingContext(document.activeElement));
  } catch {
    return false;
  }
}

/** Gives the key back to the editor command the mirrored rule shadowed. */
function runFallback(args: MonacoCommandArgs) {
  if (!args.fallback) return;
  const editor = monaco.editor.getEditors().find((candidate) => candidate.hasWidgetFocus());
  editor?.trigger("keyboard", args.fallback, null);
}

function runAppCommand(args: MonacoCommandArgs) {
  const store = useKeybindingsStore();
  if (store.recording) return;
  const context = currentKeybindingContext(document.activeElement);
  if (!runtimeWhenHolds(args) || !canRunKeybindingCommand(args.id, context)) {
    runFallback(args);
    return;
  }
  const { t } = useVueI18n();
  void executeRegisteredCommand(args.id).catch(() => {
    const entry = getKeybindingCommand(args.id);
    ElMessage.error(t("commandPalette.actionFailed", { name: entry ? t(entry.titleKey) : args.id }));
  });
}

function runEditorHandler(kind: keyof MonacoEditorHandlers, args: MonacoCommandArgs | null) {
  if (useKeybindingsStore().recording) return;
  if (args && !runtimeWhenHolds(args)) {
    runFallback(args);
    return;
  }
  // A registered workspace-wide save-all (phase 6) wins; until then it saves the focused editor.
  const handler = focusedEditor()?.handlers[kind];
  if (handler) handler();
  else if (args) runFallback(args);
}

function registerGlobalCommands() {
  monaco.editor.registerCommand(MONACO_APP_COMMAND, (_accessor: unknown, raw: unknown) => {
    const args = commandArgs(raw);
    if (args) runAppCommand(args);
  });
  monaco.editor.registerCommand(MONACO_EDITOR_SAVE_COMMAND, (_accessor: unknown, raw: unknown) => {
    runEditorHandler("save", commandArgs(raw));
  });
  monaco.editor.registerCommand(MONACO_EDITOR_RUN_COMMAND, (_accessor: unknown, raw: unknown) => {
    runEditorHandler("run", commandArgs(raw));
  });
  monaco.editor.registerCommand("mincode.diff.next", () => focusedDiffEditor()?.navigate("next"));
  monaco.editor.registerCommand("mincode.diff.previous", () => focusedDiffEditor()?.navigate("previous"));
}

/** The active editor-mode provider (only while the preset requests its mode). */
function activeEditorModeProvider(): EditorModeProvider | undefined {
  const store = useKeybindingsStore();
  return store.editorMode === "vim" ? store.editorModeProvider : undefined;
}

const MONACO_KEY_API = {
  KeyMod: monaco.KeyMod,
  // Numeric enum: name → code (its reverse code → name entries are never looked up).
  KeyCode: monaco.KeyCode as unknown as Record<string, number>,
  chord: (first: number, second: number) => monaco.KeyMod.chord(first, second),
};

function applyRules() {
  const store = useKeybindingsStore();
  const provider = activeEditorModeProvider();
  const owns = provider?.ownsSequence;
  const result = buildMonacoKeymapRules({
    platform: store.platform,
    resolution: store.resolution,
    api: MONACO_KEY_API,
    editorModeOwns: owns ? (sequence: KeySequence) => owns.call(provider, sequence, store.platform) : undefined,
  });
  appliedRules?.dispose();
  appliedRules = monaco.editor.addKeybindingRules(
    result.rules.map(({ keybinding, command, when, commandArgs: args }) => ({ keybinding, command, when, commandArgs: args })),
  );
  lastResult = result;
}

/** Installs the global Monaco commands and keeps the keymap rules in sync with the store. Idempotent. */
export function ensureMonacoKeymap(): void {
  if (installed) return;
  installed = true;
  registerGlobalCommands();
  // Contribution points that register Monaco data (snippets, editor themes) run once Monaco is loaded.
  ensureMonacoContributions(monaco);
  // Inert until the active preset requests `editorMode: "vim"`; monaco-vim itself loads lazily on attach.
  registerVimEditorMode({
    saveHandlerFor: (editor) => [...editors].find((entry) => entry.editor === editor)?.handlers.save,
  });
  const store = useKeybindingsStore();
  // Detached: the subscription outlives the host component that triggered it.
  effectScope(true).run(() => {
    watch(() => [store.resolution, store.editorMode, store.editorModeProvider] as const, applyRules, { immediate: true });
  });
}

/** The rule set currently applied (diagnostics and tests). */
export function currentMonacoKeymap(): MonacoKeymapResult | null {
  return lastResult;
}

/**
 * Registers a MinCode text editor: sets its `mincodeEditorKind` context key,
 * provides `editor.kind` to the MinCode keybinding context, routes
 * `mincode.editor.save` / `mincode.editor.run` to `handlers` while it has
 * focus, attaches the active editor-mode provider, and provides the plugin
 * menu context (`menuContext`: relative path, SQL database kind/name) to the
 * editor context menu items of services/monaco-menus.ts.
 */
export function registerMonacoEditor(
  editor: monaco.editor.IStandaloneCodeEditor,
  options: {
    kind: MonacoEditorKind;
    handlers: MonacoEditorHandlers;
    statusElement?: () => HTMLElement | null | undefined;
    menuContext?: () => MonacoMenuContext | undefined;
  },
): MonacoEditorRegistration {
  ensureMonacoKeymap();
  let kind = options.kind;
  const kindKey = editor.createContextKey<string>(MONACO_EDITOR_KIND_CONTEXT_KEY, kind);
  const entry: EditorEntry = { editor, handlers: options.handlers };
  editors.add(entry);
  const container = editor.getContainerDomNode();
  const disposeContext = registerKeybindingContext("editor.kind", (target) =>
    isInside(container, target) ? kind : undefined,
  );
  const detachMenuContext = attachMonacoMenuContext(editor, { kind: () => kind, menuContext: options.menuContext });

  let modeAttachment: EditorModeDisposable | null = null;
  let modeGeneration = 0;
  const detachMode = () => {
    modeGeneration += 1;
    modeAttachment?.dispose();
    modeAttachment = null;
  };
  const scope = effectScope(true);
  scope.run(() => {
    watch(
      activeEditorModeProvider,
      (provider) => {
        detachMode();
        if (!provider) return;
        const generation = modeGeneration;
        void Promise.resolve(provider.attach(editor, { statusElement: options.statusElement?.() ?? null })).then(
          (attachment) => {
            if (generation === modeGeneration) modeAttachment = attachment;
            else attachment.dispose();
          },
          () => undefined,
        );
      },
      { immediate: true },
    );
  });

  return {
    setKind(next) {
      kind = next;
      kindKey.set(next);
    },
    dispose() {
      scope.stop();
      detachMode();
      disposeContext();
      detachMenuContext();
      editors.delete(entry);
    },
  };
}

/** Routes `mincode.diff.next` / `mincode.diff.previous` to this diff editor while it has focus. */
export function registerMonacoDiffNavigation(
  diffEditor: monaco.editor.IStandaloneDiffEditor,
  navigate: (direction: "next" | "previous") => void,
): () => void {
  ensureMonacoKeymap();
  const entry: DiffEntry = { diffEditor, navigate };
  diffEditors.add(entry);
  const container = diffEditor.getContainerDomNode();
  const disposeContext = registerKeybindingContext("editor.kind", (target) =>
    isInside(container, target) ? "diff" : undefined,
  );
  return () => {
    disposeContext();
    diffEditors.delete(entry);
  };
}
