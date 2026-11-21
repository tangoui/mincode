/**
 * Plugin items (contributes.menus) in the Monaco editor and SQL editor
 * context menus. Monaco's context menu comes from the global
 * `monaco.editor.addEditorAction` registry, so every item is one global
 * action in the "9_plugins" group whose precondition (Monaco evaluates it as
 * the menu `when`) is the item's when clause translated by
 * domain/plugins/menus.ts `monacoMenuPrecondition`:
 *
 * - `editor.kind` → `mincodeEditorKind` (set by registerMonacoEditor),
 *   `editor.languageId` / `editor.hasSelection` / `editor.readonly` → the
 *   built-in Monaco keys, `database.kind` → per-editor `mincodeDatabaseKind`;
 * - the editor menu adds `mincodeEditorKind != 'sql'`, the SQL menu
 *   `mincodeEditorKind == 'sql'`;
 * - plugin context keys (host.context.set) → generated per-editor keys that
 *   this module keeps in sync on every registered editor;
 * - `platform` is resolved at registration.
 *
 * Actions are re-registered when the items, plugin commands, plugin contexts
 * or the UI language change. Monaco has no overflow submenu, so the
 * "More plugin actions" limit does not apply here. This module imports Monaco
 * types only: it is installed through the Monaco contribution hook and never
 * loads Monaco eagerly.
 *
 * Plugin services add their editor submenu from monaco-service-menus.ts,
 * loaded lazily with services/plugin-services.ts; this module only offers
 * the per-editor seam it needs (onMonacoEditorAttached).
 */
import { effectScope, watch, type Ref } from "vue";
import type * as MonacoRuntime from "@/services/monaco-runtime.js";
import {
  menuPassContext,
  monacoMenuPrecondition,
  sortMenuItems,
  type EditorMenuTarget,
  type PluginMenuItem,
} from "@/domain/plugins/menus";
import type { PluginContextValue } from "@/domain/commands/when";
import { useVueI18n } from "@/i18n/messages";
import { registerMonacoContributionHook, type MonacoApi } from "@/services/monaco-contributions";
import type { PluginCommandState } from "@/services/plugin-commands";

type CodeEditor = MonacoRuntime.editor.ICodeEditor;
type StandaloneEditor = MonacoRuntime.editor.IStandaloneCodeEditor;
type ContextKey = MonacoRuntime.editor.IContextKey<PluginContextValue | undefined>;

/** Per-editor menu data supplied by the editor host (see registerMonacoEditor). */
export interface MonacoMenuContext {
  /** Path relative to the project root, when the editor shows a project file. */
  relativePath?: string;
  /** SQL editor only: engine and schema name; never connection strings or credentials. */
  database?: { kind: string; name?: string };
}

export interface MonacoPluginMenuSource {
  items: Ref<readonly PluginMenuItem[]>;
  state: Ref<PluginCommandState>;
  platform: () => string;
  run: (item: PluginMenuItem, context: () => Record<string, unknown>) => Promise<void>;
  available: (item: PluginMenuItem) => boolean;
  title: (item: PluginMenuItem) => string;
}

interface EditorAttachment {
  editor: StandaloneEditor;
  kind: () => string;
  menuContext?: () => MonacoMenuContext | undefined;
  pluginKeys: Map<string, ContextKey>;
}

/** Per-editor setup added by a lazily loaded module; returns its cleanup. */
export type MonacoEditorAttachHook = (editor: StandaloneEditor) => () => void;

const MONACO_MENU_GROUP = "9_plugins";
const DATABASE_KIND_KEY = "mincodeDatabaseKind";

const attachments = new Map<CodeEditor, EditorAttachment>();
/** `${pluginId}\0${key}` → generated Monaco context key. */
const pluginKeyIds = new Map<string, string>();
let source: MonacoPluginMenuSource | undefined;
/** Attach hooks and, per hook, the cleanups of the editors it ran on. */
const attachHooks = new Map<MonacoEditorAttachHook, Map<CodeEditor, () => void>>();
let monacoApi: MonacoApi | undefined;
let actionIds = 0;
const issues: string[] = [];

/** Items that could not be expressed as a Monaco precondition (diagnostics). */
export function monacoMenuIssues(): readonly string[] {
  return issues;
}

function pluginKeyFor(pluginId: string) {
  return (key: string) => {
    const id = `${pluginId}\u0000${key}`;
    let generated = pluginKeyIds.get(id);
    if (!generated) {
      generated = `mincodePluginCtx${pluginKeyIds.size}`;
      pluginKeyIds.set(id, generated);
    }
    return generated;
  };
}

function monacoValue(value: PluginContextValue | undefined) {
  return value === null ? undefined : value;
}

/** Copies the current plugin context values into every registered editor's generated keys. */
function syncPluginKeys() {
  const contexts = source?.state.value.contexts ?? {};
  for (const [id, generated] of pluginKeyIds) {
    const [pluginId = "", key = ""] = id.split("\u0000");
    const value = monacoValue(contexts[pluginId]?.[key]);
    for (const attachment of attachments.values()) {
      const contextKey = attachment.pluginKeys.get(generated);
      if (contextKey) contextKey.set(value);
      else attachment.pluginKeys.set(generated, attachment.editor.createContextKey<PluginContextValue | undefined>(generated, value));
    }
  }
}

function editorTarget(editor: CodeEditor, attachment: EditorAttachment | undefined): EditorMenuTarget {
  const model = editor.getModel();
  const selection = editor.getSelection();
  const selected = model && selection && !selection.isEmpty() ? model.getValueInRange(selection) : "";
  const extra = attachment?.menuContext?.();
  const readOnlyOption = monacoApi?.editor.EditorOption.readOnly;
  return {
    kind: attachment?.kind() ?? "text",
    languageId: model?.getLanguageId() ?? "plaintext",
    readonly: readOnlyOption !== undefined ? Boolean(editor.getOption(readOnlyOption)) : false,
    ...(selected ? { selection: selected } : {}),
    ...(extra?.relativePath ? { relativePath: extra.relativePath } : {}),
    ...(extra?.database ? { database: extra.database } : {}),
  };
}

function registerActions(monaco: MonacoApi): () => void {
  const current = source;
  if (!current) return () => undefined;
  issues.length = 0;
  const platform = current.platform();
  const candidates = current.items.value.filter(
    (item) => (item.menu === "editor/context" || item.menu === "sql/editor/context") && current.available(item),
  );
  const titled = candidates.map((item) => ({ item, title: current.title(item), group: item.group, order: item.order, pluginName: item.pluginName }));
  const disposables: MonacoRuntime.IDisposable[] = [];
  sortMenuItems(titled, (entry) => entry.title).forEach(({ item, title }, index) => {
    let precondition: string | null;
    try {
      precondition = monacoMenuPrecondition(item, platform, pluginKeyFor(item.pluginId));
    } catch (error) {
      issues.push(`${item.pluginId}:${item.command}: ${error instanceof Error ? error.message : String(error)}`);
      return;
    }
    if (precondition === null) return;
    const menu = item.menu as "editor/context" | "sql/editor/context";
    disposables.push(
      monaco.editor.addEditorAction({
        id: `mincode.pluginMenu.${++actionIds}`,
        label: title,
        contextMenuGroupId: MONACO_MENU_GROUP,
        contextMenuOrder: index + 1,
        precondition,
        run: (editor) => {
          const attachment = attachments.get(editor);
          return current.run(item, () => menuPassContext(menu, editorTarget(editor, attachment)));
        },
      }),
    );
  });
  syncPluginKeys();
  return () => {
    for (const disposable of disposables) disposable.dispose();
  };
}

/**
 * Connects the menu consumer to Monaco. Nothing happens until Monaco loads
 * (first editor); returns a disposer that removes every registered action.
 */
export function installMonacoPluginMenus(menuSource: MonacoPluginMenuSource): () => void {
  source = menuSource;
  const unregister = registerMonacoContributionHook((monaco) => {
    monacoApi = monaco;
    let disposeActions: () => void = () => undefined;
    const scope = effectScope(true);
    const { locale } = useVueI18n();
    scope.run(() => {
      watch(
        () => [menuSource.items.value, menuSource.state.value, locale.value] as const,
        () => {
          disposeActions();
          disposeActions = registerActions(monaco);
        },
        { immediate: true },
      );
    });
    return () => {
      scope.stop();
      disposeActions();
    };
  });
  return () => {
    unregister();
    if (source === menuSource) source = undefined;
  };
}

/**
 * Runs `hook` on every attached editor now and on each one attached later;
 * the hook's cleanup runs on detach. Returns a disposer that cleans up every
 * editor the hook ran on.
 */
export function onMonacoEditorAttached(hook: MonacoEditorAttachHook): () => void {
  const cleanups = new Map<CodeEditor, () => void>();
  attachHooks.set(hook, cleanups);
  for (const attachment of attachments.values()) cleanups.set(attachment.editor, hook(attachment.editor));
  return () => {
    attachHooks.delete(hook);
    for (const cleanup of cleanups.values()) cleanup();
    cleanups.clear();
  };
}

/**
 * Attaches menu context to one editor: the `mincodeDatabaseKind` key (kept in
 * sync with `menuContext`), the generated plugin context keys and whatever
 * the attach hooks add (onMonacoEditorAttached). Called by
 * registerMonacoEditor; returns the detach function.
 */
export function attachMonacoMenuContext(
  editor: StandaloneEditor,
  options: { kind: () => string; menuContext?: () => MonacoMenuContext | undefined },
): () => void {
  const attachment: EditorAttachment = { editor, kind: options.kind, menuContext: options.menuContext, pluginKeys: new Map() };
  attachments.set(editor, attachment);
  const databaseKind = editor.createContextKey<string>(DATABASE_KIND_KEY, "");
  for (const [hook, cleanups] of attachHooks) cleanups.set(editor, hook(editor));
  const scope = effectScope(true);
  scope.run(() => {
    watch(
      () => options.menuContext?.()?.database?.kind ?? "",
      (kind) => databaseKind.set(kind),
      { immediate: true },
    );
  });
  syncPluginKeys();
  return () => {
    scope.stop();
    for (const cleanups of attachHooks.values()) {
      cleanups.get(editor)?.();
      cleanups.delete(editor);
    }
    attachments.delete(editor);
  };
}
