/**
 * 表格暂存修改（纯模型）：单元格修改、新增行、删除行先记在本地，再经 `db_apply_changes`
 * 一次提交。所有函数返回新状态，不修改入参。
 *
 * - 行键：已有行为 `pk:` + 按列名排序的主键 JSON；新增行为 `insert:<序号>`。
 * - 提交顺序：删除 → 修改 → 新增（各类内按暂存先后），`markFailed` / `dropApplied` 的序号
 *   与 `toRowChanges` 的顺序一致。
 */

/** `db_apply_changes` 的一处修改。 */
export interface RowChange {
  op: "update" | "insert" | "delete";
  primaryKey?: Record<string, unknown>;
  values?: Record<string, unknown>;
}

export type PendingChange =
  | { op: "update"; key: string; primaryKey: Record<string, unknown>; values: Record<string, unknown>; original: Record<string, unknown> }
  | { op: "insert"; key: string; values: Record<string, unknown> }
  | { op: "delete"; key: string; primaryKey: Record<string, unknown> };

export interface PendingChangesState {
  /** 按暂存先后排列。 */
  readonly changes: readonly PendingChange[];
  readonly nextInsertId: number;
  /** 上次提交出错的那处修改。 */
  readonly failedKey?: string;
}

/** 表格里一行的暂存状态（用于着色）。 */
export interface PendingRowState {
  kind: PendingChange["op"];
  /** 修改过的列（新增行为全部已填的列）。 */
  columns: ReadonlySet<string>;
  failed: boolean;
}

const OP_ORDER: Record<PendingChange["op"], number> = { delete: 0, update: 1, insert: 2 };

function jsonValue(value: unknown): string {
  if (value === undefined) return "null";
  return JSON.stringify(value, (_key, entry) => (typeof entry === "bigint" ? `${entry}n` : entry)) ?? "null";
}

function sameValue(left: unknown, right: unknown) {
  return jsonValue(left) === jsonValue(right);
}

export function primaryKeyRowKey(primaryKey: Record<string, unknown>): string {
  const entries = Object.keys(primaryKey).sort().map((column) => [column, primaryKey[column]]);
  return `pk:${jsonValue(entries)}`;
}

export function isInsertRowKey(key: string) {
  return key.startsWith("insert:");
}

export function createPendingChanges(): PendingChangesState {
  return { changes: [], nextInsertId: 1 };
}

function withChanges(state: PendingChangesState, changes: PendingChange[]): PendingChangesState {
  const failedKey = state.failedKey && changes.some((change) => change.key === state.failedKey) ? state.failedKey : undefined;
  return { changes, nextInsertId: state.nextInsertId, failedKey };
}

function without(record: Record<string, unknown>, column: string) {
  const next = { ...record };
  delete next[column];
  return next;
}

/** 暂存一个单元格：同格多次只保留最新值，原值取第一次的；回到原值时移除该列，整行无改动时移除。已标记删除的行不受影响。 */
export function stageUpdate(
  state: PendingChangesState,
  primaryKey: Record<string, unknown>,
  column: string,
  value: unknown,
  originalValue: unknown,
): PendingChangesState {
  const key = primaryKeyRowKey(primaryKey);
  const index = state.changes.findIndex((change) => change.key === key);
  const existing = index >= 0 ? state.changes[index] : undefined;
  if (existing && existing.op !== "update") return state;
  const original = existing && column in existing.original ? existing.original[column] : originalValue;
  const changes = [...state.changes];
  if (sameValue(value, original)) {
    if (!existing || !(column in existing.values)) return state;
    const values = without(existing.values, column);
    if (Object.keys(values).length) changes[index] = { ...existing, values, original: without(existing.original, column) };
    else changes.splice(index, 1);
    return withChanges(state, changes);
  }
  const next: PendingChange = {
    op: "update",
    key,
    primaryKey: existing?.primaryKey ?? { ...primaryKey },
    values: { ...existing?.values, [column]: value },
    original: { ...existing?.original, [column]: original },
  };
  if (existing) changes[index] = next;
  else changes.push(next);
  return withChanges(state, changes);
}

/** 撤销一个已修改的单元格（回到原值）；整行无改动时移除。 */
export function revertCell(state: PendingChangesState, rowKey: string, column: string): PendingChangesState {
  const index = state.changes.findIndex((change) => change.key === rowKey);
  const existing = index >= 0 ? state.changes[index] : undefined;
  if (existing?.op !== "update" || !(column in existing.values)) return state;
  return stageUpdate(state, existing.primaryKey, column, existing.original[column], existing.original[column]);
}

/** 撤销一整行的暂存（取消删除 / 丢弃新增 / 还原修改）。 */
export function revertRow(state: PendingChangesState, rowKey: string): PendingChangesState {
  if (!state.changes.some((change) => change.key === rowKey)) return state;
  return withChanges(state, state.changes.filter((change) => change.key !== rowKey));
}

export function stageInsert(state: PendingChangesState, values: Record<string, unknown>): PendingChangesState {
  const change: PendingChange = { op: "insert", key: `insert:${state.nextInsertId}`, values: { ...values } };
  return { ...withChanges(state, [...state.changes, change]), nextInsertId: state.nextInsertId + 1 };
}

/** 修改一个暂存新增行的字段。 */
export function editInsert(state: PendingChangesState, rowKey: string, column: string, value: unknown): PendingChangesState {
  const index = state.changes.findIndex((change) => change.key === rowKey);
  const existing = index >= 0 ? state.changes[index] : undefined;
  if (existing?.op !== "insert") return state;
  const changes = [...state.changes];
  changes[index] = { ...existing, values: { ...existing.values, [column]: value } };
  return withChanges(state, changes);
}

/**
 * 暂存删除一个已有行（按主键）：替换该行已暂存的修改。暂存的新增行不按主键值匹配（复制出的
 * 新增行可能与原行主键相同），删除新增行用 revertRow(新增行键)。
 */
export function stageDelete(state: PendingChangesState, primaryKey: Record<string, unknown>): PendingChangesState {
  const key = primaryKeyRowKey(primaryKey);
  if (state.changes.some((change) => change.key === key && change.op === "delete")) return state;
  const changes = state.changes.filter((change) => change.key !== key);
  changes.push({ op: "delete", key, primaryKey: { ...primaryKey } });
  return withChanges(state, changes);
}

export function pendingCount(state: PendingChangesState): number {
  return state.changes.length;
}

/** 按提交顺序（删除 → 修改 → 新增）排列的暂存修改。 */
export function orderedChanges(state: PendingChangesState): PendingChange[] {
  return state.changes
    .map((change, index) => ({ change, index }))
    .sort((left, right) => OP_ORDER[left.change.op] - OP_ORDER[right.change.op] || left.index - right.index)
    .map(({ change }) => change);
}

export function toRowChanges(state: PendingChangesState): RowChange[] {
  return orderedChanges(state).map((change) => {
    if (change.op === "delete") return { op: "delete", primaryKey: change.primaryKey };
    if (change.op === "insert") return { op: "insert", values: change.values };
    return { op: "update", primaryKey: change.primaryKey, values: change.values };
  });
}

export function summary(state: PendingChangesState): { updated: number; inserted: number; deleted: number } {
  const count = (op: PendingChange["op"]) => state.changes.filter((change) => change.op === op).length;
  return { updated: count("update"), inserted: count("insert"), deleted: count("delete") };
}

/** 标记提交出错的那处修改（序号对应 toRowChanges 的顺序；越界时清除标记）。 */
export function markFailed(state: PendingChangesState, failedIndex: number): PendingChangesState {
  return { ...state, failedKey: orderedChanges(state)[failedIndex]?.key };
}

/** 移除按提交顺序已生效的前 applied 处修改（非事务库部分成功）。 */
export function dropApplied(state: PendingChangesState, applied: number): PendingChangesState {
  if (applied <= 0) return state;
  const done = new Set(orderedChanges(state).slice(0, applied).map((change) => change.key));
  return withChanges(state, state.changes.filter((change) => !done.has(change.key)));
}

/**
 * 提交部分失败后的暂存：前 applied 处已生效（只有逐条执行的非事务库会大于 0）的从暂存移除，
 * 失败序号随之前移后标记；后端没给失败序号（failedIndex 为 undefined，插件驱动）时无法知道
 * 哪些已生效，整组原样保留。
 */
export function afterPartialFailure(
  state: PendingChangesState,
  applied: number,
  failedIndex: number | undefined,
): PendingChangesState {
  if (failedIndex === undefined) return state;
  return markFailed(dropApplied(state, applied), failedIndex - Math.max(applied, 0));
}

/** 暂存所写入的目标；第一处修改暂存时记录，提交时必须与当前一致。 */
export interface PendingTarget {
  connectionId: string;
  sessionId: string;
  database: string;
  table: string;
  namespace: string;
}

export function sameTarget(left: PendingTarget | undefined, right: PendingTarget | undefined) {
  return Boolean(left && right) &&
    left!.connectionId === right!.connectionId &&
    left!.sessionId === right!.sessionId &&
    left!.database === right!.database &&
    left!.table === right!.table &&
    left!.namespace === right!.namespace;
}

export type PendingSubmitResult =
  | { outcome: "cancelled"; state: PendingChangesState }
  | { outcome: "submitted"; state: PendingChangesState; applied: number; refreshError?: unknown }
  | { outcome: "failed"; state: PendingChangesState; error: unknown };

/**
 * 提交暂存：apply 发送修改，后端答复成功后（刷新表格之前）调用 committed。一旦 committed，
 * 暂存一律清空，之后的错误（刷新失败）只作 refreshError 报告——绝不能保留已生效的修改，
 * 否则再按一次提交会重复写入。partial 从抛出的错误中取出部分失败信息（其他错误为 undefined）。
 */
export async function submitPendingChanges(
  state: PendingChangesState,
  apply: (changes: RowChange[], committed: () => void) => Promise<{ applied: number } | undefined>,
  partial: (cause: unknown) => { applied: number; failedIndex?: number } | undefined,
  onCommitted?: () => void,
): Promise<PendingSubmitResult> {
  let committed = false;
  const commit = () => {
    if (committed) return;
    committed = true;
    onCommitted?.();
  };
  try {
    const result = await apply(toRowChanges(state), commit);
    if (!result) return committed ? { outcome: "submitted", state: createPendingChanges(), applied: pendingCount(state) } : { outcome: "cancelled", state };
    commit();
    return { outcome: "submitted", state: createPendingChanges(), applied: result.applied };
  } catch (cause) {
    if (committed) return { outcome: "submitted", state: createPendingChanges(), applied: pendingCount(state), refreshError: cause };
    const failure = partial(cause);
    return { outcome: "failed", state: failure ? afterPartialFailure(state, failure.applied, failure.failedIndex) : state, error: cause };
  }
}

/**
 * 把暂存修改叠加到已加载的行上：修改替换单元格值，删除只标记，新增追加在末尾。
 * 返回的 rowKeys / states 以叠加后的行序号为索引。
 */
export function applyPendingChanges(
  rows: readonly unknown[][],
  columns: readonly string[],
  primaryKeys: readonly string[],
  state: PendingChangesState,
): { rows: unknown[][]; rowKeys: string[]; states: Map<number, PendingRowState> } {
  const byKey = new Map(state.changes.map((change) => [change.key, change]));
  const columnIndex = new Map(columns.map((column, index) => [column, index]));
  const out: unknown[][] = [];
  const rowKeys: string[] = [];
  const states = new Map<number, PendingRowState>();
  const pkIndexes = primaryKeys.map((column) => columnIndex.get(column) ?? -1);
  for (const row of rows) {
    const key = pkIndexes.every((index) => index >= 0)
      ? primaryKeyRowKey(Object.fromEntries(primaryKeys.map((column, i) => [column, row[pkIndexes[i]]])))
      : "";
    const change = key ? byKey.get(key) : undefined;
    let next = row;
    if (change?.op === "update") {
      next = [...row];
      for (const [column, value] of Object.entries(change.values)) {
        const index = columnIndex.get(column);
        if (index !== undefined) next[index] = value;
      }
    }
    if (change) {
      states.set(out.length, {
        kind: change.op,
        columns: new Set(change.op === "update" ? Object.keys(change.values) : []),
        failed: change.key === state.failedKey,
      });
    }
    rowKeys.push(key);
    out.push(next);
  }
  for (const change of state.changes) {
    if (change.op !== "insert") continue;
    states.set(out.length, { kind: "insert", columns: new Set(Object.keys(change.values)), failed: change.key === state.failedKey });
    rowKeys.push(change.key);
    out.push(columns.map((column) => change.values[column]));
  }
  return { rows: out, rowKeys, states };
}
