/**
 * Plugin services (contributes.services, surface `selection`) in the code
 * editor context menu: a "Process with plugin" submenu (not in the SQL
 * editor, like editor/context) while text is selected. Loaded only with
 * services/plugin-services.ts, which is itself loaded lazily, so none of this
 * is in the entry bundle; monaco-menus.ts supplies the per-editor seam
 * (onMonacoEditorAttached) for the selection size key. Imports Monaco types
 * only and never loads Monaco eagerly.
 */
import { effectScope, watch, type Ref } from "vue";
import type * as MonacoRuntime from "@/services/monaco-runtime.js";
import {
  SERVICE_LIMITS,
  servicesForSelection,
  utf8ByteLength,
  type PluginServiceEntry,
} from "@/domain/plugins/services";
import { useVueI18n } from "@/i18n/messages";
import { registerMonacoContributionHook, type MonacoApi } from "@/services/monaco-contributions";
import { onMonacoEditorAttached } from "@/services/monaco-menus";
import type { PluginServiceRunRequest, SelectionTarget } from "@/services/plugin-services";

type CodeEditor = MonacoRuntime.editor.ICodeEditor;
type StandaloneEditor = MonacoRuntime.editor.IStandaloneCodeEditor;

/** UTF-8 size of the primary selection; any value above every text limit stands for "too large". */
const SELECTION_BYTES_KEY = "mincodeSelectionBytes";
const SERVICE_MENU_GROUP = "9_services";
const SERVICE_SUBMENU_ID = "pluginServices";
/** Code editor only, with a selection: the editor/context rule plus editorHasSelection. */
const SERVICE_MENU_WHEN = "editorHasSelection && mincodeEditorKind != 'sql'";

export interface MonacoServiceMenuSource {
  services: Readonly<Ref<readonly PluginServiceEntry[]>>;
  run: (request: PluginServiceRunRequest) => Promise<void>;
}

let serviceSource: MonacoServiceMenuSource | undefined;
let actionIds = 0;

/**
 * UTF-8 size of the editor's primary selection, exact up to the largest text
 * limit; a longer selection reports its UTF-16 length, a lower bound that
 * already exceeds every limit, so huge selections are never copied here.
 */
function selectionBytes(editor: CodeEditor) {
  const model = editor.getModel();
  const selection = editor.getSelection();
  if (!model || !selection || selection.isEmpty()) return 0;
  const length = model.getValueLengthInRange(selection);
  return length > SERVICE_LIMITS.textMaxBytes ? length : utf8ByteLength(model.getValueInRange(selection));
}

/**
 * Where a text result goes: the selected range, tracked by a decoration so
 * edits made while the service runs do not misplace it. Each edit is one
 * undo step (pushEditOperations between stack elements).
 */
function selectionTarget(monaco: MonacoApi, model: MonacoRuntime.editor.ITextModel, range: MonacoRuntime.IRange): SelectionTarget {
  const [decoration] = model.deltaDecorations([], [
    { range, options: { stickiness: monaco.editor.TrackedRangeStickiness.NeverGrowsWhenTypingAtEdges } },
  ]);
  const apply = (edit: (current: MonacoRuntime.IRange) => MonacoRuntime.editor.IIdentifiedSingleEditOperation) => {
    if (model.isDisposed()) return;
    const current = model.getDecorationRange(decoration);
    if (!current) return;
    model.pushStackElement();
    model.pushEditOperations(null, [edit(current)], () => null);
    model.pushStackElement();
  };
  return {
    replace: (text) => apply((current) => ({ range: current, text })),
    insertBelow: (text) => apply((current) => {
      // A selection of whole lines ends at column 1 of the next line: insert after its last selected line.
      const line = current.endColumn === 1 && current.endLineNumber > current.startLineNumber ? current.endLineNumber - 1 : current.endLineNumber;
      const column = model.getLineMaxColumn(line);
      return { range: new monaco.Range(line, column, line, column), text: `${model.getEOL()}${text}` };
    }),
  };
}

function runSelectionService(monaco: MonacoApi, current: MonacoServiceMenuSource, editor: CodeEditor, service: string) {
  const model = editor.getModel();
  const selection = editor.getSelection();
  if (!model || !selection || selection.isEmpty()) return Promise.resolve();
  const input = model.getValueInRange(selection);
  const readOnlyOption = monaco.editor.EditorOption.readOnly;
  // A read-only editor gets no replace/insert target: the result can still be copied or saved.
  const readOnly = Boolean(editor.getOption(readOnlyOption));
  const request: PluginServiceRunRequest = { service, surface: "selection", input };
  if (!readOnly) request.selection = selectionTarget(monaco, model, selection);
  return current.run(request);
}

/**
 * Registers the "Process with plugin" submenu: one item per selection service
 * (merged across providers). Each service has a shown-and-enabled item while
 * the selection fits some provider's input.maxBytes and a shown-but-disabled
 * "(selection too large)" item otherwise; both run the same command, whose
 * precondition also requires the selection to fit.
 */
function registerServiceActions(monaco: MonacoApi): () => void {
  const current = serviceSource;
  const menus = monaco.mincodeEditorContextMenu;
  if (!current || !menus) return () => undefined;
  const { t, localeChain } = useVueI18n();
  const groups = servicesForSelection(current.services.value, "", localeChain.value);
  if (!groups.length) return () => undefined;
  const disposables: MonacoRuntime.IDisposable[] = [
    menus.appendSubmenu({ id: SERVICE_SUBMENU_ID, title: t("plugins.services.menu"), group: SERVICE_MENU_GROUP, order: 1, when: SERVICE_MENU_WHEN }),
  ];
  groups.forEach((group, index) => {
    const limit = Math.max(...group.providers.map((entry) => entry.input.maxBytes ?? SERVICE_LIMITS.textDefaultBytes));
    const fits = `${SELECTION_BYTES_KEY} <= ${limit}`;
    const id = `mincode.pluginService.${++actionIds}`;
    disposables.push(
      monaco.editor.addEditorAction({
        id,
        label: group.title,
        precondition: `${SERVICE_MENU_WHEN} && ${fits}`,
        run: (editor) => runSelectionService(monaco, current, editor, group.service),
      }),
      menus.appendItem(SERVICE_SUBMENU_ID, {
        command: id,
        title: group.title,
        ...(group.providers[0]?.description ? { tooltip: group.providers[0].description } : {}),
        when: fits,
        order: index + 1,
      }),
      menus.appendItem(SERVICE_SUBMENU_ID, {
        command: id,
        title: t("plugins.services.tooLargeItem", { title: group.title }),
        tooltip: t("plugins.services.tooLarge"),
        when: `${SELECTION_BYTES_KEY} > ${limit}`,
        precondition: "false",
        order: index + 1,
      }),
    );
  });
  return () => {
    for (const disposable of disposables) disposable.dispose();
  };
}

/**
 * Connects plugin services to the editor context menu. Nothing happens until
 * Monaco loads; re-registers when the services or the UI language change.
 * Returns a disposer.
 */
export function installMonacoServiceMenus(menuSource: MonacoServiceMenuSource): () => void {
  serviceSource = menuSource;
  const detachSelectionKeys = onMonacoEditorAttached(trackSelectionBytes);
  const unregister = registerMonacoContributionHook((monaco) => {
    let disposeActions: () => void = () => undefined;
    const scope = effectScope(true);
    const { locale } = useVueI18n();
    scope.run(() => {
      watch(
        () => [menuSource.services.value, locale.value] as const,
        () => {
          disposeActions();
          disposeActions = registerServiceActions(monaco);
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
    detachSelectionKeys();
    if (serviceSource === menuSource) serviceSource = undefined;
  };
}

/** Keeps one editor's `mincodeSelectionBytes` key in sync with its selection; returns the cleanup. */
function trackSelectionBytes(editor: StandaloneEditor): () => void {
  const bytesKey = editor.createContextKey<number>(SELECTION_BYTES_KEY, selectionBytes(editor));
  const updateBytes = () => bytesKey.set(selectionBytes(editor));
  const listeners = [editor.onDidChangeCursorSelection(updateBytes), editor.onDidChangeModel(updateBytes)];
  return () => {
    for (const listener of listeners) listener.dispose();
    bytesKey.reset();
  };
}

