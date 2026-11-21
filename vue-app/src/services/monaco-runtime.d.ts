import type { CancellationToken, IDisposable, editor, json, languages } from "monaco-editor";

export * from "monaco-editor";

/** Monaco's JSON language defaults (schemas, diagnostics); owned by the jsonValidation consumer. */
export declare const mincodeJsonDefaults: typeof json.jsonDefaults;

/**
 * Monaco's formatter conflict resolver. mode 1 = explicit, 2 = silent (e.g. format on save);
 * kind 1 = file, 2 = selection. The newest selector wins; dispose removes it.
 */
export type MincodeFormatterSelector = <
  T extends languages.DocumentFormattingEditProvider | languages.DocumentRangeFormattingEditProvider,
>(
  formatters: T[],
  document: editor.ITextModel,
  mode: 1 | 2,
  kind: 1 | 2
) => Promise<T | undefined>;

export declare const mincodeFormattingConflicts: {
  setFormatterSelector(selector: MincodeFormatterSelector): IDisposable;
};

/** Merged document symbols of every registered provider; powers the Outline panel. */
export declare const mincodeDocumentSymbols: {
  has(model: editor.ITextModel): boolean;
  get(model: editor.ITextModel, token: CancellationToken): Promise<languages.DocumentSymbol[]>;
  onDidChange(listener: () => void): IDisposable;
};

export declare const mincodeSaveFormatting: {
  run(model: editor.ITextModel,
    choose: (providers: languages.DocumentFormattingEditProvider[]) => languages.DocumentFormattingEditProvider | undefined,
    signal: AbortSignal): Promise<void>;
};

/** A submenu in the editor context menu (MenuId.EditorContext). */
export interface MincodeEditorSubmenu {
  /** Stable submenu id, unique per MinCode feature. */
  id: string;
  title: string;
  group: string;
  order: number;
  /** Context key expression: when the submenu is shown. */
  when?: string;
}

/** An item of a MinCode submenu that runs a registered command (e.g. from addEditorAction). */
export interface MincodeEditorSubmenuItem {
  command: string;
  title: string;
  tooltip?: string;
  /** Context key expression: when the item is shown. */
  when?: string;
  /** Context key expression: when the item is enabled; a shown item that fails it is disabled. */
  precondition?: string;
  order: number;
}

export declare const mincodeEditorContextMenu: {
  appendSubmenu(submenu: MincodeEditorSubmenu): IDisposable;
  appendItem(submenuId: string, item: MincodeEditorSubmenuItem): IDisposable;
};
