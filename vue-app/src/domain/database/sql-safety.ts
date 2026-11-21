/**
 * SQL 安全网领域模型：与后端 `package sqlsafety` 同名的检查结果、结构化错误、
 * 留底记录与设置类型，以及解析、确认目标比对等纯函数（不依赖 UI）。
 *
 * 后端写操作必须携带 `db_safety_check` 签发的一次性 `checkId`；被拒绝时错误消息为
 * `sqlsafety:` + JSON（见 `SqlSafetyError`）。
 */

export type SqlSafetyRisk = "none" | "low" | "high";

/**
 * 写操作的发起方：user 由用户在 MinCode 中直接发起；ai 由 AI 工具发起（低风险沿用工具审批，
 * 不再弹框；高风险在任何审批模式下都由用户确认）；preconfirmed 是已经过专用确认界面的内部结构
 * 调用（设计器、改字符集、导入、Redis 编辑器），低风险不再弹框。
 */
export type SqlSafetyOrigin = "user" | "ai" | "preconfirmed";

/**
 * 带原因的确认，对任何 origin、任何风险都必须弹出：reconfirm（实际行数越界或无法留底，已回滚）；
 * forceWrite（只读执行被数据库判为写入）；aiRowThreshold（AI 单行工具在本次对话累计的行数达到
 * 阈值，check 是前端合成的，estimate.rows 为累计行数）。
 */
export type SqlSafetyConfirmNotice = "reconfirm" | "forceWrite" | "aiRowThreshold";

export const SQL_SAFETY_KINDS = ["read", "data", "insert", "structure", "procedure", "script", "unknown", "rows"] as const;

export type SqlSafetyKind = (typeof SQL_SAFETY_KINDS)[number];

export const SQL_SAFETY_REASONS = [
  "overThreshold",
  "noWhere",
  "alwaysTrueWhere",
  "procedure",
  "noEstimate",
  "unparsed",
  "noPrimaryKey",
  "cascade",
  "trigger",
  "overSnapshotLimit",
  "noTransaction",
  "notUndoableStore",
  "structure",
  "explicitTransaction",
  "estimated",
  "rowChangedTwice",
  "tableNotFound",
] as const;

export type SqlSafetyReason = (typeof SQL_SAFETY_REASONS)[number];

export interface SqlSafetyTableRef {
  schema: string;
  name: string;
}

export interface SqlSafetyEstimate {
  rows: number;
  exact: boolean;
  /** "count" 精确 COUNT(*)，"explain" 执行计划估算，"none" 无法估算。 */
  source: "count" | "explain" | "none";
}

export interface SqlSafetyStatementCheck {
  index: number;
  /** 后端截断到 2000 字符。 */
  text: string;
  kind: SqlSafetyKind;
  table?: SqlSafetyTableRef;
  estimate: SqlSafetyEstimate;
  risk: SqlSafetyRisk;
  reasons: SqlSafetyReason[];
  undoable: boolean;
}

export interface SqlSafetyCheck {
  /** 一次性令牌；只读文本为空串。 */
  checkId: string;
  sessionId: string;
  database: string;
  statements: SqlSafetyStatementCheck[];
  risk: SqlSafetyRisk;
  /** 高风险时必须输入的名称（表名、过程名、库名或字面量 CONFIRM）。 */
  confirmTarget: string;
  undoable: boolean;
  expiresAt: string;
}

export const SQL_SAFETY_ERROR_CODES = [
  "checkRequired",
  "checkInvalid",
  "confirmMismatch",
  "reconfirm",
  "partial",
  "previewStale",
  "tableMissing",
] as const;

export type SqlSafetyErrorCode = (typeof SQL_SAFETY_ERROR_CODES)[number];

export interface SqlSafetyError {
  code: SqlSafetyErrorCode;
  check?: SqlSafetyCheck;
  /** partial：已生效的写入条数。 */
  applied?: number;
  /** partial：失败语句的序号（从 0 开始，按提交顺序）。 */
  failedIndex?: number;
  /** partial：数据库返回的错误。 */
  message?: string;
}

export const SQL_SAFETY_UNDO_STATES = ["available", "undone", "partial", "uncertain", "none"] as const;

/** uncertain：提交结果未知（可能未生效），后端拒绝撤回。 */
export type SqlSafetyUndoState = (typeof SQL_SAFETY_UNDO_STATES)[number];

/** 记录来源（Record.source）；未列出的新来源原样显示。 */
export const SQL_SAFETY_SOURCES = ["query", "grid", "ai", "import", "destructive", "undo", "designer", "e2e"] as const;

export type SqlSafetySource = (typeof SQL_SAFETY_SOURCES)[number];

export interface SqlSafetyRecord {
  id: string;
  createdAt: string;
  connectionId: string;
  connectionName: string;
  dbType: string;
  database: string;
  table: SqlSafetyTableRef;
  statement: string;
  kind: SqlSafetyKind;
  /** query grid ai import destructive undo designer */
  source: string;
  affected: number;
  undoable: boolean;
  undoReason?: SqlSafetyReason;
  undoState: SqlSafetyUndoState;
  parentId?: string;
  primaryKey?: string[];
  columns?: string[];
  rowFiles: number;
}

export interface SqlSafetySettings {
  enabled: boolean;
  highRiskRows: number;
  retentionDays: number;
  maxSnapshotRows: number;
  maxTotalBytes: number;
}

const MIB = 2 ** 20;
const GIB = 2 ** 30;

/** 设置取值范围，与 Go `sqlsafety.Settings.Validate` 一致（总量上限单位为字节）。 */
export const SQL_SAFETY_SETTINGS_LIMITS = {
  highRiskRows: { min: 1, max: 1_000_000 },
  retentionDays: { min: 1, max: 30 },
  maxSnapshotRows: { min: 1, max: 100_000 },
  maxTotalBytes: { min: 64 * MIB, max: 20 * GIB },
} as const;

export type SqlSafetySettingsField = keyof typeof SQL_SAFETY_SETTINGS_LIMITS;

const SETTINGS_FIELDS = Object.keys(SQL_SAFETY_SETTINGS_LIMITS) as SqlSafetySettingsField[];

/** 返回超出范围或不是整数的字段（按固定顺序）；空数组表示可以保存。 */
export function validateSafetySettings(settings: SqlSafetySettings): SqlSafetySettingsField[] {
  return SETTINGS_FIELDS.filter((field) => {
    const value: unknown = settings[field];
    const { min, max } = SQL_SAFETY_SETTINGS_LIMITS[field];
    return typeof value !== "number" || !Number.isSafeInteger(value) || value < min || value > max;
  });
}

/** 界面以 GiB 编辑总量上限，后端按字节保存。 */
export function safetyGibToBytes(gib: number): number {
  return Math.round(gib * GIB);
}

export function safetyBytesToGib(bytes: number): number {
  return bytes / GIB;
}

/** 校验后端返回的设置；结构不符时抛错（取值范围由后端保证，界面另用 validateSafetySettings）。 */
export function parseSqlSafetySettings(value: unknown): SqlSafetySettings {
  if (!isObject(value)) throw new Error("Malformed SQL safety settings");
  if (typeof value.enabled !== "boolean") throw new Error("Malformed SQL safety settings: enabled");
  const settings: SqlSafetySettings = {
    enabled: value.enabled,
    highRiskRows: 0,
    retentionDays: 0,
    maxSnapshotRows: 0,
    maxTotalBytes: 0,
  };
  for (const field of SETTINGS_FIELDS) {
    const number = value[field];
    if (typeof number !== "number" || !Number.isFinite(number)) throw new Error(`Malformed SQL safety settings: ${field}`);
    settings[field] = number;
  }
  return settings;
}

/** 没有表名可输入时，高风险确认要求输入的字面量（与 Go `ConfirmLiteral` 一致）。 */
export const SQL_SAFETY_CONFIRM_LITERAL = "CONFIRM";

const RISKS: readonly SqlSafetyRisk[] = ["none", "low", "high"];
const KINDS: readonly SqlSafetyKind[] = SQL_SAFETY_KINDS;
const ESTIMATE_SOURCES: readonly SqlSafetyEstimate["source"][] = ["count", "explain", "none"];

type JsonObject = Record<string, unknown>;

function isObject(value: unknown): value is JsonObject {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function oneOf<T extends string>(value: unknown, allowed: readonly T[], field: string): T {
  if (typeof value === "string" && (allowed as readonly string[]).includes(value)) return value as T;
  throw new Error(`Malformed SQL safety check: ${field}`);
}

function text(value: unknown, field: string): string {
  if (value === undefined || value === null) return "";
  if (typeof value !== "string") throw new Error(`Malformed SQL safety check: ${field}`);
  return value;
}

function count(value: unknown, field: string): number {
  if (value === undefined) return 0;
  if (typeof value !== "number" || !Number.isFinite(value)) throw new Error(`Malformed SQL safety check: ${field}`);
  return value;
}

function parseTable(value: unknown): SqlSafetyTableRef | undefined {
  if (value === undefined || value === null) return undefined;
  if (!isObject(value)) throw new Error("Malformed SQL safety check: table");
  return { schema: text(value.schema, "table.schema"), name: text(value.name, "table.name") };
}

function parseReasons(value: unknown): SqlSafetyReason[] {
  if (value === undefined || value === null) return [];
  if (!Array.isArray(value)) throw new Error("Malformed SQL safety check: reasons");
  // 原因只用于说明；未知的新原因不影响风险判定（风险由后端给出），直接忽略。
  return value.filter((reason): reason is SqlSafetyReason =>
    typeof reason === "string" && (SQL_SAFETY_REASONS as readonly string[]).includes(reason));
}

function parseStatement(value: unknown): SqlSafetyStatementCheck {
  if (!isObject(value)) throw new Error("Malformed SQL safety check: statement");
  const estimate = isObject(value.estimate) ? value.estimate : {};
  const statement: SqlSafetyStatementCheck = {
    index: count(value.index, "statement.index"),
    text: text(value.text, "statement.text"),
    kind: oneOf(value.kind, KINDS, "statement.kind"),
    estimate: {
      rows: count(estimate.rows, "estimate.rows"),
      exact: estimate.exact === true,
      source: estimate.source === undefined ? "none" : oneOf(estimate.source, ESTIMATE_SOURCES, "estimate.source"),
    },
    risk: oneOf(value.risk, RISKS, "statement.risk"),
    reasons: parseReasons(value.reasons),
    undoable: value.undoable === true,
  };
  const table = parseTable(value.table);
  if (table) statement.table = table;
  return statement;
}

/** 校验并规范化后端返回的 Check；结构不符时抛错。 */
export function parseSqlSafetyCheck(value: unknown): SqlSafetyCheck {
  if (!isObject(value)) throw new Error("Malformed SQL safety check");
  if (value.statements !== undefined && value.statements !== null && !Array.isArray(value.statements)) {
    throw new Error("Malformed SQL safety check: statements");
  }
  const statements = Array.isArray(value.statements) ? value.statements : [];
  return {
    checkId: text(value.checkId, "checkId"),
    sessionId: text(value.sessionId, "sessionId"),
    database: text(value.database, "database"),
    statements: statements.map(parseStatement),
    risk: oneOf(value.risk, RISKS, "risk"),
    confirmTarget: text(value.confirmTarget, "confirmTarget"),
    undoable: value.undoable === true,
    expiresAt: text(value.expiresAt, "expiresAt"),
  };
}

function errorText(error: unknown): string | undefined {
  if (typeof error === "string") return error;
  if (error instanceof Error) return error.message;
  if (isObject(error) && typeof error.message === "string") return error.message;
  return undefined;
}

/** 后端总以 `sqlsafety:` 开头；只允许运行时加上的 "Error: " 这类包装。 */
const ERROR_PATTERN = /^(?:[A-Za-z]*Error:\s*)?sqlsafety:/;

/**
 * 从原生调用的拒绝值中解析 `sqlsafety:` 结构化错误；普通错误或格式不符返回 undefined。
 * 前缀必须在消息开头（可带 "Error: " 包装）：数据库或驱动报错文本中间出现的 `sqlsafety:`
 * （如触发器 RAISE）不算，以免伪造确认框或部分成功。
 */
export function parseSqlSafetyError(error: unknown): SqlSafetyError | undefined {
  const message = errorText(error);
  const match = message ? ERROR_PATTERN.exec(message) : null;
  if (!message || !match) return undefined;
  let body: unknown;
  try {
    body = JSON.parse(message.slice(match[0].length));
  } catch {
    return undefined;
  }
  if (!isObject(body) || !(SQL_SAFETY_ERROR_CODES as readonly unknown[]).includes(body.code)) return undefined;
  const parsed: SqlSafetyError = { code: body.code as SqlSafetyErrorCode };
  try {
    if (body.check !== undefined && body.check !== null) parsed.check = parseSqlSafetyCheck(body.check);
  } catch {
    return undefined;
  }
  if (typeof body.applied === "number") parsed.applied = body.applied;
  if (typeof body.failedIndex === "number") parsed.failedIndex = body.failedIndex;
  if (typeof body.message === "string") parsed.message = body.message;
  return parsed;
}

function normalizeConfirmName(value: string): string {
  return value.replace(/[`"[\]]/g, "").trim().toLowerCase();
}

function splitConfirmName(value: string): [schema: string, name: string] {
  const dot = value.lastIndexOf(".");
  return dot >= 0 ? [value.slice(0, dot).trim(), value.slice(dot + 1).trim()] : ["", value];
}

/**
 * 高风险确认输入比对，与 Go `sqlsafety.ConfirmTargetMatches` 一致：去掉 ` " [ ] 引号、
 * 去首尾空白、不区分大小写；两边都带 schema 时 schema 必须相同，任一边省略 schema 只比表名。
 */
export function confirmTargetMatches(target: string, typed: string): boolean {
  const t = normalizeConfirmName(target);
  const y = normalizeConfirmName(typed);
  if (!t || !y) return false;
  if (t === y) return true;
  const [targetSchema, targetName] = splitConfirmName(t);
  const [typedSchema, typedName] = splitConfirmName(y);
  return targetName === typedName && (targetSchema === "" || typedSchema === "");
}

/**
 * 撤回记录是否涉及某个打开的表标签：同连接、同库（不分大小写；记录没有库名时不比较），表名按
 * 确认目标的规则比较（去引号、不分大小写、可带可不带 schema）。
 */
export function safetyRecordTouchesTable(
  record: Pick<SqlSafetyRecord, "connectionId" | "database" | "table">,
  tab: { connectionId: string; database: string; table: string },
): boolean {
  if (record.connectionId !== tab.connectionId || !record.table.name) return false;
  if (record.database && tab.database && record.database.toLowerCase() !== tab.database.toLowerCase()) return false;
  const name = record.table.schema ? `${record.table.schema}.${record.table.name}` : record.table.name;
  return confirmTargetMatches(name, tab.table);
}

export function reasonMessageKey(reason: SqlSafetyReason): `database.safety.reason.${SqlSafetyReason}` {
  return `database.safety.reason.${reason}`;
}

export type SqlSafetyEstimateMessage =
  | { key: "database.safety.exactRows" | "database.safety.estimateRows"; count: number }
  | { key: "database.safety.unknownRows" };

/** 行数说明：精确计数、估算（约）或无法估算。 */
export function estimateMessage(estimate: SqlSafetyEstimate): SqlSafetyEstimateMessage {
  if (estimate.source === "none") return { key: "database.safety.unknownRows" };
  return { key: estimate.exact ? "database.safety.exactRows" : "database.safety.estimateRows", count: estimate.rows };
}

// ── 操作记录与撤回 ──

export type SqlSafetyUndoMode = "skipConflicts" | "overwrite";

export type SqlSafetyConflictKind = "changed" | "missing" | "exists";

export interface SqlSafetyUndoConflict {
  primaryKey: Record<string, unknown>;
  kind: SqlSafetyConflictKind;
}

/** `db_safety_undo_preview` 的结果；conflicts 最多列 1000 条，conflictCount 为总数。 */
export interface SqlSafetyUndoPreview {
  record: SqlSafetyRecord;
  conflicts: SqlSafetyUndoConflict[];
  conflictCount: number;
  total: number;
  /** db_safety_undo 必须带上用户看到的这次预览的令牌。 */
  previewToken: string;
}

export interface SqlSafetyUndoResult {
  restored: number;
  skipped: number;
  recordId: string;
  recordIds: string[];
}

/** `db_safety_history_get`：记录与前 20 行留底样例（行按 columns 排列）。 */
export interface SqlSafetyHistoryDetail {
  record: SqlSafetyRecord;
  sample: { columns: string[]; before: unknown[][]; after: unknown[][] };
}

const CONFLICT_KINDS: readonly SqlSafetyConflictKind[] = ["changed", "missing", "exists"];

function malformed(field: string): Error {
  return new Error(`Malformed SQL safety record: ${field}`);
}

function stringList(value: unknown, field: string): string[] {
  if (value === undefined || value === null) return [];
  if (!Array.isArray(value) || value.some((item) => typeof item !== "string")) throw malformed(field);
  return value as string[];
}

function rowList(value: unknown, field: string): unknown[][] {
  if (value === undefined || value === null) return [];
  if (!Array.isArray(value) || value.some((row) => !Array.isArray(row))) throw malformed(field);
  return value as unknown[][];
}

function recordCount(value: unknown, field: string): number {
  if (value === undefined || value === null) return 0;
  if (typeof value !== "number" || !Number.isFinite(value)) throw malformed(field);
  return value;
}

function recordText(value: unknown, field: string): string {
  if (value === undefined || value === null) return "";
  if (typeof value !== "string") throw malformed(field);
  return value;
}

/** 校验并规范化一条操作记录；未知的新 kind / 状态 / 原因按 unknown / none / 无处理。 */
export function parseSqlSafetyRecord(value: unknown): SqlSafetyRecord {
  if (!isObject(value)) throw malformed("record");
  const id = recordText(value.id, "id");
  if (!id) throw malformed("id");
  const table = isObject(value.table) ? value.table : {};
  const kind = (KINDS as readonly unknown[]).includes(value.kind) ? (value.kind as SqlSafetyKind) : "unknown";
  const undoState = (SQL_SAFETY_UNDO_STATES as readonly unknown[]).includes(value.undoState)
    ? (value.undoState as SqlSafetyUndoState)
    : "none";
  const parsed: SqlSafetyRecord = {
    id,
    createdAt: recordText(value.createdAt, "createdAt"),
    connectionId: recordText(value.connectionId, "connectionId"),
    connectionName: recordText(value.connectionName, "connectionName"),
    dbType: recordText(value.dbType, "dbType"),
    database: recordText(value.database, "database"),
    table: { schema: recordText(table.schema, "table.schema"), name: recordText(table.name, "table.name") },
    statement: recordText(value.statement, "statement"),
    kind,
    source: recordText(value.source, "source"),
    affected: recordCount(value.affected, "affected"),
    undoable: value.undoable === true,
    undoState,
    rowFiles: recordCount(value.rowFiles, "rowFiles"),
  };
  if ((SQL_SAFETY_REASONS as readonly unknown[]).includes(value.undoReason)) {
    parsed.undoReason = value.undoReason as SqlSafetyReason;
  }
  const parentId = recordText(value.parentId, "parentId");
  if (parentId) parsed.parentId = parentId;
  if (value.primaryKey !== undefined && value.primaryKey !== null) parsed.primaryKey = stringList(value.primaryKey, "primaryKey");
  if (value.columns !== undefined && value.columns !== null) parsed.columns = stringList(value.columns, "columns");
  return parsed;
}

export function parseSqlSafetyHistoryList(value: unknown): SqlSafetyRecord[] {
  if (value === undefined || value === null) return [];
  if (!Array.isArray(value)) throw malformed("history");
  return value.map(parseSqlSafetyRecord);
}

export function parseSqlSafetyHistoryDetail(value: unknown): SqlSafetyHistoryDetail {
  if (!isObject(value)) throw malformed("detail");
  const sample = isObject(value.sample) ? value.sample : {};
  return {
    record: parseSqlSafetyRecord(value.record),
    sample: {
      columns: stringList(sample.columns, "sample.columns"),
      before: rowList(sample.before, "sample.before"),
      after: rowList(sample.after, "sample.after"),
    },
  };
}

export function parseSqlSafetyUndoPreview(value: unknown): SqlSafetyUndoPreview {
  if (!isObject(value)) throw malformed("preview");
  const previewToken = recordText(value.previewToken, "previewToken");
  if (!previewToken) throw malformed("previewToken");
  if (value.conflicts !== undefined && value.conflicts !== null && !Array.isArray(value.conflicts)) throw malformed("conflicts");
  const conflicts = (Array.isArray(value.conflicts) ? value.conflicts : []).map((conflict): SqlSafetyUndoConflict => {
    if (!isObject(conflict)) throw malformed("conflict");
    return {
      primaryKey: isObject(conflict.primaryKey) ? conflict.primaryKey : {},
      kind: CONFLICT_KINDS.includes(conflict.kind as SqlSafetyConflictKind) ? (conflict.kind as SqlSafetyConflictKind) : "changed",
    };
  });
  return {
    record: parseSqlSafetyRecord(value.record),
    conflicts,
    conflictCount: Math.max(recordCount(value.conflictCount, "conflictCount"), conflicts.length),
    total: recordCount(value.total, "total"),
    previewToken,
  };
}

export function parseSqlSafetyUndoResult(value: unknown): SqlSafetyUndoResult {
  if (!isObject(value)) throw malformed("undo result");
  return {
    restored: recordCount(value.restored, "restored"),
    skipped: recordCount(value.skipped, "skipped"),
    recordId: recordText(value.recordId, "recordId"),
    recordIds: stringList(value.recordIds, "recordIds"),
  };
}

export type SqlSafetyTagType = "success" | "info" | "warning" | "danger" | "primary";

export interface SqlSafetyHistoryRowView {
  stateKey: `database.safety.history.state.${SqlSafetyUndoState}`;
  stateType: SqlSafetyTagType;
  kindKey: `database.safety.history.kind.${SqlSafetyKind}`;
  /** 未知来源为 undefined（界面原样显示 source）。 */
  sourceKey?: `database.safety.history.source.${SqlSafetySource}`;
  canUndo: boolean;
  undoLabelKey: "database.safety.history.undo" | "database.safety.history.undoRemaining";
  /** 撤回不可用的原因（文案键）。 */
  disabledReasonKey?: SqlSafetyHistoryDisabledKey;
  /** 可撤回记录附带的提醒（如级联、无事务留底、插件提交后留底）。 */
  noticeKey?: `database.safety.reason.${SqlSafetyReason}`;
}

export type SqlSafetyHistoryDisabledKey =
  | "database.safety.history.uncertain"
  | "database.safety.history.alreadyUndone"
  | "database.safety.history.notUndoable"
  | "database.safety.history.notConnected"
  | `database.safety.reason.${SqlSafetyReason}`;

const STATE_TYPES: Record<SqlSafetyUndoState, SqlSafetyTagType> = {
  available: "success",
  partial: "warning",
  undone: "info",
  uncertain: "danger",
  none: "info",
};

/** 操作记录一行的显示：状态标签、能否撤回、按钮文案与禁用原因。 */
export function historyRowView(record: SqlSafetyRecord): SqlSafetyHistoryRowView {
  const view: SqlSafetyHistoryRowView = {
    stateKey: `database.safety.history.state.${record.undoState}`,
    stateType: STATE_TYPES[record.undoState],
    kindKey: `database.safety.history.kind.${record.kind}`,
    canUndo: false,
    undoLabelKey: record.undoState === "partial" ? "database.safety.history.undoRemaining" : "database.safety.history.undo",
  };
  if ((SQL_SAFETY_SOURCES as readonly string[]).includes(record.source)) {
    view.sourceKey = `database.safety.history.source.${record.source as SqlSafetySource}`;
  }
  if (record.undoState === "uncertain") {
    view.disabledReasonKey = "database.safety.history.uncertain";
  } else if (record.undoState === "undone") {
    view.disabledReasonKey = "database.safety.history.alreadyUndone";
  } else if (!record.undoable) {
    view.disabledReasonKey = record.undoReason ? reasonMessageKey(record.undoReason) : "database.safety.history.notUndoable";
  } else if (record.undoState === "none") {
    view.disabledReasonKey = "database.safety.history.notUndoable";
  } else {
    view.canUndo = true;
  }
  if (record.undoable && record.undoReason) view.noticeKey = reasonMessageKey(record.undoReason);
  return view;
}

/**
 * 撤回大约会写多少行（用于决定是否要输入表名）：skipConflicts 只写无冲突的行；overwrite 还写
 * changed / exists 的行，missing 总是跳过；撤回 INSERT 不覆盖，冲突行总是跳过。
 * conflicts 被截断时按“未列出的冲突都会写”多估，宁可多要一次确认。
 */
export function undoWriteEstimate(preview: SqlSafetyUndoPreview, mode: SqlSafetyUndoMode): number {
  if (mode === "skipConflicts" || preview.record.kind === "insert") {
    return Math.max(0, preview.total - preview.conflictCount);
  }
  const missing = preview.conflicts.filter((conflict) => conflict.kind === "missing").length;
  return Math.max(0, preview.total - missing);
}

/**
 * 预计写入行数超过高风险阈值时要输入的表名，未超过返回 undefined；阈值未知（读取失败）时
 * 按超过处理，避免用户什么都没输入就被后端以 confirmMismatch 拒绝。
 */
export function undoConfirmTarget(
  preview: SqlSafetyUndoPreview,
  mode: SqlSafetyUndoMode,
  highRiskRows: number | undefined,
): string | undefined {
  if (highRiskRows !== undefined && undoWriteEstimate(preview, mode) <= highRiskRows) return undefined;
  const { schema, name } = preview.record.table;
  if (!name) return SQL_SAFETY_CONFIRM_LITERAL;
  return schema ? `${schema}.${name}` : name;
}

const SAMPLE_CELL_LIMIT = 120;

/**
 * 留底样例单元格的显示文本：NULL、数字、布尔原样；字符串（后端把二进制、时间都转成了字符串）
 * 转义控制字符并截断；对象 / 数组用 JSON。
 */
export function sampleCellText(value: unknown, limit = SAMPLE_CELL_LIMIT): string {
  if (value === null || value === undefined) return "NULL";
  let text: string;
  if (typeof value === "string") {
    text = value;
  } else if (typeof value === "number" || typeof value === "boolean" || typeof value === "bigint") {
    text = String(value);
  } else {
    try {
      text = JSON.stringify(value) ?? String(value);
    } catch {
      text = String(value);
    }
  }
  // eslint-disable-next-line no-control-regex
  text = text.replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, (char) =>
    `\\x${char.charCodeAt(0).toString(16).padStart(2, "0")}`);
  return text.length > limit ? `${text.slice(0, limit)}…` : text;
}

/** 冲突行的主键文本，如 "id=3, region=eu"。 */
export function undoConflictKeyText(primaryKey: Record<string, unknown>): string {
  return Object.entries(primaryKey).map(([column, value]) => `${column}=${sampleCellText(value, 60)}`).join(", ");
}
