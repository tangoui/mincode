/**
 * Nodes of the lazy variable tree shared by the Variables pane, the debug
 * hover and the Evaluate dialog, and how their values are shown.
 */
import type { DapVariable } from "./types.ts";

/** Characters of a value shown in a row; the full value is in the tooltip and copied. */
export const VALUE_DISPLAY_LIMIT = 300;
const VALUE_TITLE_LIMIT = 4000;

/** Most nodes one refresh expands again on its own (a step stays fast). */
export const RESTORE_EXPAND_LIMIT = 64;
/** Deepest tree level (roots are level 1) a refresh expands again on its own. */
export const RESTORE_EXPAND_DEPTH = 8;

export interface DebugVariableNode {
  /** The node's `path`: el-tree's node key. */
  key: string;
  /**
   * Where the node sits, stable across stops (unlike `reference`): a scope is
   * `scope:<name>`, a watch `watch:<index>:<expression>`, an Evaluate result
   * `result`, a variable its parent's path plus its name (`variableNodePath`).
   */
  path: string;
  name: string;
  value?: string;
  type?: string;
  /** variablesReference; 0 has no children. */
  reference: number;
  /**
   * The variablesReference of the container the variable was read from (its
   * DAP setVariable target); 0 for scope, watch and result nodes.
   */
  parentReference: number;
  /** `result`: an evaluated expression (Evaluate dialog). */
  kind: "watch" | "scope" | "variable" | "result";
  error?: boolean;
  /** Watch nodes: the expression's index in the stored list. */
  watchIndex?: number;
  leaf: boolean;
}

/**
 * The path of a child: `siblingIndex` counts the earlier siblings of the same
 * name, so the second `x` is `<parent>/x#2`.
 */
export function variableNodePath(parentPath: string, name: string, siblingIndex: number): string {
  const path = `${parentPath}/${encodeURIComponent(name)}`;
  return siblingIndex > 0 ? `${path}#${siblingIndex + 1}` : path;
}

/** For each name, how many earlier entries have the same name (`variableNodePath`'s `siblingIndex`). */
export function siblingIndexes(names: readonly string[]): number[] {
  const seen = new Map<string, number>();
  return names.map((name) => {
    const index = seen.get(name) ?? 0;
    seen.set(name, index + 1);
    return index;
  });
}

/** `siblingIndex`: see `variableNodePath`; `parentReference`: the container's variablesReference. */
export function variableNode(variable: DapVariable, parentPath: string, siblingIndex: number, parentReference: number): DebugVariableNode {
  const path = variableNodePath(parentPath, variable.name, siblingIndex);
  return {
    key: path,
    path,
    name: variable.name,
    value: variable.value,
    type: variable.type,
    reference: variable.variablesReference,
    parentReference,
    kind: "variable",
    leaf: !(variable.variablesReference > 0),
  };
}

export function displayValue(value: string | undefined, limit = VALUE_DISPLAY_LIMIT): string {
  if (value === undefined) return "";
  return value.length > limit ? `${value.slice(0, limit)}…` : value;
}

export function titleValue(value: string | undefined): string | undefined {
  if (value === undefined) return undefined;
  return value.length > VALUE_TITLE_LIMIT ? `${value.slice(0, VALUE_TITLE_LIMIT)}…` : value;
}
