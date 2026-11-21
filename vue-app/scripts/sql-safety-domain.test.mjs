import assert from "node:assert/strict";
import test from "node:test";

import {
  confirmTargetMatches,
  estimateMessage,
  historyRowView,
  parseSqlSafetyCheck,
  parseSqlSafetyError,
  parseSqlSafetyHistoryDetail,
  parseSqlSafetyRecord,
  parseSqlSafetySettings,
  parseSqlSafetyUndoPreview,
  parseSqlSafetyUndoResult,
  safetyBytesToGib,
  safetyGibToBytes,
  safetyRecordTouchesTable,
  sampleCellText,
  SQL_SAFETY_SETTINGS_LIMITS,
  SQL_SAFETY_KINDS,
  SQL_SAFETY_SOURCES,
  SQL_SAFETY_UNDO_STATES,
  undoConfirmTarget,
  undoConflictKeyText,
  undoWriteEstimate,
  validateSafetySettings,
  reasonMessageKey,
  SQL_SAFETY_CONFIRM_LITERAL,
  SQL_SAFETY_ERROR_CODES,
  SQL_SAFETY_REASONS,
} from "../src/domain/database/sql-safety.ts";
import { messagesZh as zh } from "../src/i18n/locales/zh.ts";
import { messagesEn as en } from "../src/i18n/locales/en.ts";

const check = {
  checkId: "chk-1",
  sessionId: "s1",
  database: "shop",
  statements: [{
    index: 0,
    text: "DELETE FROM orders",
    kind: "data",
    table: { schema: "", name: "orders" },
    estimate: { rows: 5000, exact: true, source: "count" },
    risk: "high",
    reasons: ["noWhere", "overThreshold"],
    undoable: true,
  }],
  risk: "high",
  confirmTarget: "orders",
  undoable: true,
  expiresAt: "2026-10-31T00:05:00Z",
};

test("parses a structured sqlsafety error carrying a check", () => {
  const parsed = parseSqlSafetyError(new Error(`sqlsafety:${JSON.stringify({ code: "reconfirm", check })}`));
  assert.equal(parsed?.code, "reconfirm");
  assert.equal(parsed?.check?.checkId, "chk-1");
  assert.equal(parsed?.check?.statements[0].table?.name, "orders");
  assert.deepEqual(parsed?.check?.statements[0].reasons, ["noWhere", "overThreshold"]);
});

test("parses string rejections, wrapped prefixes and partial fields", () => {
  const parsed = parseSqlSafetyError(`Error: sqlsafety:{"code":"partial","applied":2,"failedIndex":2,"message":"duplicate key"}`);
  assert.deepEqual(parsed, { code: "partial", applied: 2, failedIndex: 2, message: "duplicate key" });
  assert.deepEqual(parseSqlSafetyError({ message: 'sqlsafety:{"code":"checkRequired"}' }), { code: "checkRequired" });
});

test("sqlsafety must start the message, so database text cannot forge one", () => {
  const forged = 'sqlsafety:{"code":"partial","applied":3}';
  assert.equal(parseSqlSafetyError(new Error(`trigger raised: ${forged}`)), undefined);
  assert.equal(parseSqlSafetyError(`driver_error: ERROR: ${forged} (SQLSTATE P0001)`), undefined);
  assert.equal(parseSqlSafetyError(`Error: something else sqlsafety:{"code":"checkRequired"}`), undefined);
  assert.deepEqual(parseSqlSafetyError(`TypeError: ${forged}`), { code: "partial", applied: 3 });
});

test("ordinary and malformed errors are not sqlsafety errors", () => {
  assert.equal(parseSqlSafetyError(new Error("no such table: orders")), undefined);
  assert.equal(parseSqlSafetyError("sqlsafety:{not json"), undefined);
  assert.equal(parseSqlSafetyError('sqlsafety:{"code":"somethingElse"}'), undefined);
  assert.equal(parseSqlSafetyError(undefined), undefined);
  assert.equal(parseSqlSafetyError(new Error(`sqlsafety:${JSON.stringify({ code: "reconfirm", check: { risk: "maybe" } })}`)), undefined);
});

test("parseSqlSafetyCheck validates the payload and drops unknown reasons", () => {
  const parsed = parseSqlSafetyCheck({ ...check, statements: [{ ...check.statements[0], reasons: ["noWhere", "future"], table: undefined }] });
  assert.deepEqual(parsed.statements[0].reasons, ["noWhere"]);
  assert.equal(parsed.statements[0].table, undefined);
  assert.throws(() => parseSqlSafetyCheck({ ...check, statements: "x" }));
  assert.throws(() => parseSqlSafetyCheck(null));
});

test("parseSqlSafetyCheck keeps the tableNotFound reason the Go check sends for a missing target table", () => {
  const parsed = parseSqlSafetyCheck({ ...check, statements: [{ ...check.statements[0], reasons: ["noEstimate", "tableNotFound"] }] });
  assert.deepEqual(parsed.statements[0].reasons, ["noEstimate", "tableNotFound"]);
});

test("confirmTargetMatches follows the Go rule (quotes, case, optional schema)", () => {
  const cases = [
    ["Order", "`order`", true],
    ["dbo.Order", "[Order]", true],
    ["Order", "Orders", false],
    ["Order", ' public."ORDER" ', true],
    ["dbo.Order", "[dbo].[Order]", true],
    ["dbo.Order", "sales.Order", false],
    ["Order", "", false],
    ["", "", false],
    [SQL_SAFETY_CONFIRM_LITERAL, "confirm", true],
    [SQL_SAFETY_CONFIRM_LITERAL, " CONFIRM ", true],
    [SQL_SAFETY_CONFIRM_LITERAL, "CONFIRMED", false],
  ];
  for (const [target, typed, want] of cases) {
    assert.equal(confirmTargetMatches(target, typed), want, `${target} / ${typed}`);
  }
});

test("reasons map to database.safety.reason keys present in zh and en", () => {
  assert.equal(reasonMessageKey("cascade"), "database.safety.reason.cascade");
  for (const reason of SQL_SAFETY_REASONS) {
    const key = reasonMessageKey(reason);
    assert.ok(zh[key], `zh ${key}`);
    assert.ok(en[key], `en ${key}`);
  }
  assert.match(zh["database.safety.reason.cascade"], /撤回只能恢复本表，关联表的改动无法恢复/);
});

test("estimate message distinguishes exact, estimated and unknown counts", () => {
  assert.deepEqual(estimateMessage({ rows: 12, exact: true, source: "count" }), { key: "database.safety.exactRows", count: 12 });
  assert.deepEqual(estimateMessage({ rows: 900, exact: false, source: "explain" }), { key: "database.safety.estimateRows", count: 900 });
  assert.deepEqual(estimateMessage({ rows: 0, exact: false, source: "none" }), { key: "database.safety.unknownRows" });
  assert.equal(zh["database.safety.estimateRows"], "约 {count} 行");
  assert.equal(en["database.safety.estimateRows"], "~{count} rows");
  assert.equal(zh["database.safety.typeTableName"], "请输入表名 {name} 以确认");
  assert.equal(zh["database.safety.partial"], "已执行 {applied} 条，第 {index} 条失败，后续未执行");
});

const record = {
  id: "r1",
  createdAt: "2026-10-31T08:00:00Z",
  connectionId: "c1",
  connectionName: "local",
  dbType: "mysql",
  database: "shop",
  table: { schema: "", name: "orders" },
  statement: "DELETE FROM orders WHERE id < 10",
  kind: "data",
  source: "query",
  affected: 9,
  undoable: true,
  undoState: "available",
  primaryKey: ["id"],
  columns: ["id", "name"],
  rowFiles: 1,
};

test("parses a history record and keeps optional fields only when present", () => {
  const parsed = parseSqlSafetyRecord({ ...record, undoReason: "noTransaction", parentId: "r0" });
  assert.equal(parsed.id, "r1");
  assert.equal(parsed.undoReason, "noTransaction");
  assert.equal(parsed.parentId, "r0");
  assert.deepEqual(parsed.columns, ["id", "name"]);
  const bare = parseSqlSafetyRecord({ id: "r2", kind: "rows", undoState: "none", undoable: false, table: { name: "t" } });
  assert.equal(bare.undoReason, undefined);
  assert.equal(bare.affected, 0);
  assert.equal(bare.table.schema, "");
  // Unknown reason / kind / state from a newer backend do not break the list.
  const future = parseSqlSafetyRecord({ ...record, undoReason: "somethingNew", kind: "graph", undoState: "weird" });
  assert.equal(future.undoReason, undefined);
  assert.equal(future.kind, "unknown");
  assert.equal(future.undoState, "none");
  assert.throws(() => parseSqlSafetyRecord(null), /Malformed/);
  assert.throws(() => parseSqlSafetyRecord({ ...record, id: "" }), /Malformed/);
});

test("history row view: available and partial records can be undone", () => {
  const available = historyRowView(parseSqlSafetyRecord(record));
  assert.equal(available.canUndo, true);
  assert.equal(available.undoLabelKey, "database.safety.history.undo");
  assert.equal(available.disabledReasonKey, undefined);
  assert.equal(available.stateKey, "database.safety.history.state.available");
  assert.equal(available.kindKey, "database.safety.history.kind.data");
  assert.equal(available.sourceKey, "database.safety.history.source.query");
  const partial = historyRowView(parseSqlSafetyRecord({ ...record, undoState: "partial" }));
  assert.equal(partial.canUndo, true);
  assert.equal(partial.undoLabelKey, "database.safety.history.undoRemaining");
});

test("history row view: undone, uncertain and not-undoable records are disabled with a reason", () => {
  const undone = historyRowView(parseSqlSafetyRecord({ ...record, undoState: "undone" }));
  assert.equal(undone.canUndo, false);
  assert.equal(undone.disabledReasonKey, "database.safety.history.alreadyUndone");

  const uncertain = historyRowView(parseSqlSafetyRecord({ ...record, undoState: "uncertain" }));
  assert.equal(uncertain.canUndo, false);
  assert.equal(uncertain.disabledReasonKey, "database.safety.history.uncertain");
  assert.equal(uncertain.stateType, "danger");

  const noPk = historyRowView(parseSqlSafetyRecord({ ...record, undoable: false, undoState: "none", undoReason: "noPrimaryKey" }));
  assert.equal(noPk.canUndo, false);
  assert.equal(noPk.disabledReasonKey, "database.safety.reason.noPrimaryKey");
  assert.equal(noPk.noticeKey, undefined);

  const bare = historyRowView(parseSqlSafetyRecord({ ...record, undoable: false, undoState: "none" }));
  assert.equal(bare.canUndo, false);
  assert.equal(bare.disabledReasonKey, "database.safety.history.notUndoable");

  // An undoable record's reason is a notice (plugin backup after commit, cascade …).
  const notice = historyRowView(parseSqlSafetyRecord({ ...record, undoReason: "cascade" }));
  assert.equal(notice.canUndo, true);
  assert.equal(notice.noticeKey, "database.safety.reason.cascade");

  const unknownSource = historyRowView(parseSqlSafetyRecord({ ...record, source: "elsewhere" }));
  assert.equal(unknownSource.sourceKey, undefined);
});

test("history labels exist in zh and en for every state, kind and source", () => {
  const keys = [
    ...SQL_SAFETY_UNDO_STATES.map((state) => `database.safety.history.state.${state}`),
    ...SQL_SAFETY_KINDS.map((kind) => `database.safety.history.kind.${kind}`),
    ...SQL_SAFETY_SOURCES.map((source) => `database.safety.history.source.${source}`),
    ...["changed", "missing", "exists"].map((kind) => `database.safety.history.conflict.${kind}`),
    "database.safety.history.undo",
    "database.safety.history.undoRemaining",
    "database.safety.history.alreadyUndone",
    "database.safety.history.uncertain",
    "database.safety.history.notUndoable",
    "database.safety.history.tableMissing",
  ];
  for (const key of keys) {
    assert.ok(zh[key], `zh ${key}`);
    assert.ok(en[key], `en ${key}`);
  }
  assert.equal(zh["database.safety.history.kind.rows"], "表格修改");
  assert.equal(zh["database.safety.history.uncertain"], "提交结果未知，无法确定是否已生效");
  assert.equal(zh["database.safety.history.undoRemaining"], "撤回剩余行");
  assert.match(zh["database.safety.history.tableMissing"], /请先恢复表结构/);
});

test("parses history detail, undo preview and undo result", () => {
  const detail = parseSqlSafetyHistoryDetail({
    record,
    sample: { columns: ["id", "name"], before: [[1, "a"]], after: [] },
  });
  assert.equal(detail.record.id, "r1");
  assert.deepEqual(detail.sample.before, [[1, "a"]]);
  assert.deepEqual(detail.sample.after, []);
  const empty = parseSqlSafetyHistoryDetail({ record, sample: { columns: null, before: null, after: null } });
  assert.deepEqual(empty.sample, { columns: [], before: [], after: [] });

  const preview = parseSqlSafetyUndoPreview({
    record,
    conflicts: [{ primaryKey: { id: 3 }, kind: "changed" }, { primaryKey: { id: 4 }, kind: "missing" }],
    conflictCount: 2,
    total: 9,
    previewToken: "tok",
  });
  assert.equal(preview.previewToken, "tok");
  assert.equal(preview.conflicts[1].kind, "missing");
  assert.throws(() => parseSqlSafetyUndoPreview({ record, conflicts: [], total: 1 }), /previewToken/);

  assert.deepEqual(parseSqlSafetyUndoResult({ restored: 7, skipped: 2, recordId: "u1", recordIds: ["u1"] }),
    { restored: 7, skipped: 2, recordId: "u1", recordIds: ["u1"] });
  assert.deepEqual(parseSqlSafetyUndoResult({ restored: 0, skipped: 0, recordId: "", recordIds: null }),
    { restored: 0, skipped: 0, recordId: "", recordIds: [] });
});

test("undo write estimate follows the mode and the record kind", () => {
  const preview = parseSqlSafetyUndoPreview({
    record,
    conflicts: [
      { primaryKey: { id: 1 }, kind: "changed" },
      { primaryKey: { id: 2 }, kind: "missing" },
      { primaryKey: { id: 3 }, kind: "exists" },
    ],
    conflictCount: 3,
    total: 10,
    previewToken: "tok",
  });
  assert.equal(undoWriteEstimate(preview, "skipConflicts"), 7);
  assert.equal(undoWriteEstimate(preview, "overwrite"), 9); // missing rows are always skipped
  const inserted = { ...preview, record: { ...preview.record, kind: "insert" } };
  assert.equal(undoWriteEstimate(inserted, "overwrite"), 7); // an INSERT undo never overwrites
});

test("undo confirm target is the table when the estimate is over the threshold", () => {
  const preview = parseSqlSafetyUndoPreview({ record, conflicts: [], conflictCount: 0, total: 1500, previewToken: "t" });
  assert.equal(undoConfirmTarget(preview, "skipConflicts", 1000), "orders");
  assert.equal(undoConfirmTarget(preview, "skipConflicts", 1500), undefined);
  const schemaPreview = { ...preview, record: { ...preview.record, table: { schema: "public", name: "orders" } } };
  assert.equal(undoConfirmTarget(schemaPreview, "skipConflicts", 10), "public.orders");
  // Unknown threshold (settings unreadable): ask for the name rather than risk confirmMismatch.
  assert.equal(undoConfirmTarget(preview, "skipConflicts", undefined), "orders");
});

test("sample cells and conflict keys render safely", () => {
  assert.equal(sampleCellText(null), "NULL");
  assert.equal(sampleCellText(undefined), "NULL");
  assert.equal(sampleCellText(12.5), "12.5");
  assert.equal(sampleCellText(true), "true");
  assert.equal(sampleCellText("2026-10-31T08:00:00Z"), "2026-10-31T08:00:00Z");
  assert.equal(sampleCellText("a\u0000b\u0007"), "a\\x00b\\x07");
  assert.equal(sampleCellText("x".repeat(300)), `${"x".repeat(120)}…`);
  assert.equal(sampleCellText("x".repeat(30), 10), `${"x".repeat(10)}…`);
  assert.equal(sampleCellText({ a: 1 }), '{"a":1}');
  assert.equal(sampleCellText([1, 2]), "[1,2]");
  assert.equal(undoConflictKeyText({ id: 3, region: "eu" }), "id=3, region=eu");
  assert.equal(undoConflictKeyText({}), "");
});

test("every database.safety key in zh exists in en and vice versa", () => {
  const zhKeys = Object.keys(zh).filter((key) => key.startsWith("database.safety.")).sort();
  const enKeys = Object.keys(en).filter((key) => key.startsWith("database.safety.")).sort();
  assert.deepEqual(enKeys, zhKeys);
  for (const code of SQL_SAFETY_ERROR_CODES) {
    if (code === "partial" || code === "reconfirm") continue; // own top-level messages
    assert.ok(zh[`database.safety.error.${code}`], `zh error.${code}`);
    assert.ok(en[`database.safety.error.${code}`], `en error.${code}`);
  }
});

const validSettings = (overrides = {}) => ({
  enabled: true, highRiskRows: 1000, retentionDays: 7, maxSnapshotRows: 100000, maxTotalBytes: 2 * 2 ** 30, ...overrides,
});

test("settings limits match the Go Settings.Validate ranges", () => {
  assert.deepEqual(SQL_SAFETY_SETTINGS_LIMITS, {
    highRiskRows: { min: 1, max: 1_000_000 },
    retentionDays: { min: 1, max: 30 },
    maxSnapshotRows: { min: 1, max: 100_000 },
    maxTotalBytes: { min: 64 * 2 ** 20, max: 20 * 2 ** 30 },
  });
});

test("validateSafetySettings accepts the defaults and every boundary value", () => {
  assert.deepEqual(validateSafetySettings(validSettings()), []);
  for (const [field, { min, max }] of Object.entries(SQL_SAFETY_SETTINGS_LIMITS)) {
    assert.deepEqual(validateSafetySettings(validSettings({ [field]: min })), [], `${field} min`);
    assert.deepEqual(validateSafetySettings(validSettings({ [field]: max })), [], `${field} max`);
  }
  assert.deepEqual(validateSafetySettings(validSettings({ enabled: false })), []);
});

test("validateSafetySettings rejects values just outside the range, fractions and non-numbers", () => {
  for (const [field, { min, max }] of Object.entries(SQL_SAFETY_SETTINGS_LIMITS)) {
    for (const value of [min - 1, max + 1, min + 0.5, Number.NaN, Number.POSITIVE_INFINITY, null, undefined, "5"]) {
      assert.deepEqual(validateSafetySettings(validSettings({ [field]: value })), [field], `${field} = ${String(value)}`);
    }
  }
  assert.deepEqual(
    validateSafetySettings(validSettings({ highRiskRows: 0, retentionDays: 31, maxSnapshotRows: 100_001, maxTotalBytes: 64 * 2 ** 20 - 1 })),
    ["highRiskRows", "retentionDays", "maxSnapshotRows", "maxTotalBytes"],
  );
});

test("total backup limit converts between GiB and bytes at the UI range edges", () => {
  assert.equal(safetyGibToBytes(0.0625), 64 * 2 ** 20);
  assert.equal(safetyGibToBytes(20), 20 * 2 ** 30);
  assert.equal(safetyGibToBytes(2), 2 * 2 ** 30);
  assert.equal(safetyGibToBytes(1.5), 1.5 * 2 ** 30);
  assert.ok(Number.isSafeInteger(safetyGibToBytes(0.1)));
  assert.equal(safetyBytesToGib(64 * 2 ** 20), 0.0625);
  assert.equal(safetyBytesToGib(2 * 2 ** 30), 2);
  assert.equal(safetyBytesToGib(20 * 2 ** 30), 20);
});

test("parseSqlSafetySettings keeps the five fields and rejects malformed payloads", () => {
  assert.deepEqual(parseSqlSafetySettings({ ...validSettings(), extra: 1 }), validSettings());
  assert.deepEqual(parseSqlSafetySettings(validSettings({ enabled: false })), validSettings({ enabled: false }));
  assert.throws(() => parseSqlSafetySettings(null), /Malformed SQL safety settings/);
  assert.throws(() => parseSqlSafetySettings({ ...validSettings(), enabled: "yes" }), /Malformed SQL safety settings: enabled/);
  assert.throws(() => parseSqlSafetySettings({ ...validSettings(), retentionDays: "7" }), /Malformed SQL safety settings: retentionDays/);
});

test("settings labels exist in zh and en", () => {
  for (const key of [
    "settings.cat.database",
    "database.safety.settings.title",
    "database.safety.settings.enabled",
    "database.safety.settings.disabledWarning",
    "database.safety.settings.highRiskRows",
    "database.safety.settings.retentionDays",
    "database.safety.settings.retentionDaysHint",
    "database.safety.settings.maxSnapshotRows",
    "database.safety.settings.maxTotalGib",
    "database.safety.settings.outOfRange",
    "database.safety.settings.loadFailed",
    "database.safety.settings.saveFailed",
    "database.safety.settings.saved",
  ]) {
    assert.ok(zh[key], `zh ${key}`);
    assert.ok(en[key], `en ${key}`);
  }
  assert.match(zh["database.safety.settings.disabledWarning"], /只保留通用确认框，不留底、不能撤回/);
});

test("safetyRecordTouchesTable matches the open table tab an undo rewrote", () => {
  const record = { connectionId: "c1", database: "shop", table: { schema: "public", name: "Orders" } };
  const tab = (overrides = {}) => ({ connectionId: "c1", database: "shop", table: "orders", ...overrides });
  assert.equal(safetyRecordTouchesTable(record, tab()), true);
  assert.equal(safetyRecordTouchesTable(record, tab({ table: "public.orders" })), true);
  assert.equal(safetyRecordTouchesTable(record, tab({ database: "SHOP" })), true);
  assert.equal(safetyRecordTouchesTable({ ...record, database: "" }, tab({ database: "other" })), true);
  assert.equal(safetyRecordTouchesTable(record, tab({ table: "audit.orders" })), false);
  assert.equal(safetyRecordTouchesTable(record, tab({ table: "items" })), false);
  assert.equal(safetyRecordTouchesTable(record, tab({ database: "other" })), false);
  assert.equal(safetyRecordTouchesTable(record, tab({ connectionId: "c2" })), false);
  assert.equal(safetyRecordTouchesTable({ ...record, table: { schema: "", name: "" } }, tab()), false);
});
