import assert from "node:assert/strict";
import test from "node:test";

import {
  applyBackupRunEvent,
  backupFileBaseName,
  backupJobFromDraft,
  backupNotificationKind,
  createBackupJobDraft,
  draftFromBackupJob,
  formatBackupBytes,
  formatBackupDuration,
  isBackupCandidateConnection,
  parseBackupJobView,
  parseBackupJobViews,
  parseBackupRunEvent,
  parseBackupRuns,
  validateBackupJobDraft,
} from "../src/domain/database/backup-jobs.ts";

function validDraft(overrides = {}) {
  return {
    ...createBackupJobDraft("conn-1", "app"),
    name: "Nightly",
    destination: "/Users/me/backups",
    ...overrides,
  };
}

function run(overrides = {}) {
  return { id: "r1", jobId: "job-1", trigger: "schedule", status: "success", startedAt: 1_700_000_000_000, ...overrides };
}

test("new drafts default to a valid daily gzip job once named and given a folder", () => {
  const draft = createBackupJobDraft("conn-1", "app");
  assert.deepEqual(draft.databases, ["app"]);
  assert.equal(draft.scheduleKind, "daily");
  assert.equal(draft.gzip, true);
  assert.deepEqual(validateBackupJobDraft(draft).sort(), ["destination", "name"]);
  assert.deepEqual(validateBackupJobDraft(validDraft()), []);
});

test("validation covers schedule, targets, folder and retention", () => {
  assert.deepEqual(validateBackupJobDraft(validDraft({ connectionId: "" })), ["connection"]);
  assert.deepEqual(validateBackupJobDraft(validDraft({ databases: [" "] })), ["databases"]);
  assert.deepEqual(
    validateBackupJobDraft(validDraft({ databases: ["a", "b"], tables: ["users"] })),
    ["tablesNeedOneDatabase"],
  );
  assert.deepEqual(validateBackupJobDraft(validDraft({ scheduleKind: "interval", intervalHours: 0 })), ["interval"]);
  assert.deepEqual(validateBackupJobDraft(validDraft({ scheduleKind: "interval", intervalHours: 1.5 })), ["interval"]);
  assert.deepEqual(validateBackupJobDraft(validDraft({ scheduleKind: "interval", intervalHours: 169 })), ["interval"]);
  assert.deepEqual(validateBackupJobDraft(validDraft({ scheduleKind: "interval", intervalHours: 6, time: "bad" })), []);
  assert.deepEqual(validateBackupJobDraft(validDraft({ time: "24:00" })), ["time"]);
  assert.deepEqual(validateBackupJobDraft(validDraft({ time: "9:00" })), ["time"]);
  assert.deepEqual(validateBackupJobDraft(validDraft({ scheduleKind: "weekly", weekdays: [] })), ["weekdays"]);
  assert.deepEqual(validateBackupJobDraft(validDraft({ scheduleKind: "weekly", weekdays: [7] })), ["weekdays"]);
  assert.deepEqual(validateBackupJobDraft(validDraft({ destination: "relative/dir" })), ["destination"]);
  assert.deepEqual(validateBackupJobDraft(validDraft({ destination: "C:\\Backups" })), []);
  assert.deepEqual(validateBackupJobDraft(validDraft({ keepLast: -1 })), ["retention"]);
  assert.deepEqual(validateBackupJobDraft(validDraft({ keepDays: 3651 })), ["retention"]);
});

test("drafts serialize to the save payload without backend-owned fields", () => {
  const payload = backupJobFromDraft(
    validDraft({
      name: "  Nightly  ",
      tables: ["users", " users ", "orders", ""],
      scheduleKind: "weekly",
      weekdays: [5, 1, 5, 9],
      time: "03:30",
      destination: " /data/bak ",
      keepDays: 30,
    }),
  );
  assert.deepEqual(payload, {
    id: "",
    name: "Nightly",
    connectionId: "conn-1",
    targets: [{ database: "app", tables: ["users", "orders"] }],
    schedule: { kind: "weekly", time: "03:30", weekdays: [1, 5] },
    destination: "/data/bak",
    fileNamePrefix: "",
    gzip: true,
    format: "sql",
    retention: { keepLast: 7, keepDays: 30 },
    enabled: true,
    notifyOnSuccess: false,
  });
  assert.equal("state" in payload, false);
  assert.equal("createdAt" in payload, false);

  const interval = backupJobFromDraft(validDraft({ scheduleKind: "interval", intervalHours: 6, databases: ["a", "b", "a"] }));
  assert.deepEqual(interval.schedule, { kind: "interval", intervalHours: 6 });
  assert.deepEqual(interval.targets, [{ database: "a" }, { database: "b" }]);
});

test("saved jobs round-trip through the edit draft", () => {
  const view = parseBackupJobView({
    id: "job-1",
    name: "Nightly",
    connectionId: "conn-1",
    targets: [{ database: "app", tables: ["users"] }],
    schedule: { kind: "weekly", time: "02:00", weekdays: [3, 1] },
    destination: "/data/bak",
    fileNamePrefix: "Nightly",
    gzip: false,
    retention: { keepLast: 3, keepDays: 0 },
    enabled: true,
    notifyOnSuccess: true,
    createdAt: 1,
    updatedAt: 2,
    state: { lastStatus: "success", lastRunAt: 5 },
    nextRunAt: 10,
    running: false,
    lastRun: run(),
  });
  assert.ok(view);
  assert.deepEqual(view.schedule.weekdays, [1, 3]);
  assert.equal(view.lastRun?.status, "success");
  const draft = draftFromBackupJob(view);
  assert.deepEqual(draft.tables, ["users"]);
  assert.equal(draft.scheduleKind, "weekly");
  assert.equal(draft.gzip, false);
  const payload = backupJobFromDraft(draft);
  assert.deepEqual(payload.targets, view.targets);
  assert.deepEqual(payload.schedule, view.schedule);
  assert.deepEqual(payload.retention, view.retention);
  assert.equal(payload.id, "job-1");
});

test("native payloads are validated before reaching the UI", () => {
  assert.equal(parseBackupJobView(null), undefined);
  assert.equal(parseBackupJobView({ id: "x", connectionId: "c", schedule: { kind: "hourly" } }), undefined);
  const views = parseBackupJobViews([
    { id: "a", connectionId: "c", schedule: { kind: "daily", time: "01:00" }, targets: [{ database: "" }, { database: "d", tables: [1, "t"] }], state: { lastStatus: "bogus" } },
    { id: "", connectionId: "c", schedule: { kind: "daily" } },
    "junk",
  ]);
  assert.equal(views.length, 1);
  assert.deepEqual(views[0].targets, [{ database: "d", tables: ["t"] }]);
  assert.deepEqual(views[0].state, {});
  assert.deepEqual(views[0].retention, { keepLast: 0, keepDays: 0 });
  assert.equal(parseBackupJobViews({}).length, 0);

  const runs = parseBackupRuns([run({ sizeBytes: -1, rows: 12, warnings: ["w", 3] }), run({ status: "exploded" }), { id: "no-job" }]);
  assert.equal(runs.length, 1);
  assert.equal(runs[0].sizeBytes, undefined);
  assert.equal(runs[0].rows, 12);
  assert.deepEqual(runs[0].warnings, ["w"]);
});

test("run events update list state and choose notifications", () => {
  const event = parseBackupRunEvent({ jobId: "job-1", jobName: "Nightly", connectionId: "c", notifyOnSuccess: false, run: run({ status: "running" }) });
  assert.ok(event);
  assert.equal(parseBackupRunEvent({ jobId: "other", run: run() }), undefined, "run must belong to the job");

  const view = parseBackupJobView({ id: "job-1", connectionId: "c", schedule: { kind: "daily", time: "01:00" }, running: false });
  const unrelated = parseBackupJobView({ id: "job-2", connectionId: "c", schedule: { kind: "daily", time: "01:00" } });
  const [running, untouched] = applyBackupRunEvent([view, unrelated], event);
  assert.equal(running.running, true);
  assert.equal(running.state.lastStatus, undefined);
  assert.equal(untouched, unrelated);
  const [done] = applyBackupRunEvent([running], { ...event, run: run({ status: "failed", error: "boom" }) });
  assert.equal(done.running, false);
  assert.equal(done.state.lastStatus, "failed");
  assert.equal(done.lastRun?.error, "boom");

  assert.equal(backupNotificationKind(event), undefined);
  assert.equal(backupNotificationKind({ ...event, run: run({ status: "failed" }) }), "failed");
  assert.equal(backupNotificationKind({ ...event, run: run({ status: "cancelled" }) }), undefined);
  assert.equal(backupNotificationKind({ ...event, run: run() }), undefined);
  assert.equal(backupNotificationKind({ ...event, notifyOnSuccess: true, run: run() }), "success");
  assert.equal(backupNotificationKind({ ...event, run: run({ warnings: ["NULL written"] }) }), "warning");
});

test("connection filter, formatting helpers", () => {
  assert.equal(isBackupCandidateConnection({ dbType: "mysql" }), true);
  assert.equal(isBackupCandidateConnection({ dbType: "redis" }), true);
  assert.equal(isBackupCandidateConnection({ dbType: "mongodb" }), true);
  assert.equal(isBackupCandidateConnection({ dbType: "plugin", driver: { pluginId: "p", driverId: "d" } }), true);
  assert.equal(formatBackupBytes(undefined), "—");
  assert.equal(formatBackupBytes(512), "512 B");
  assert.equal(formatBackupBytes(1536), "1.5 KB");
  assert.equal(formatBackupBytes(300 * 1024 * 1024), "300 MB");
  assert.equal(formatBackupDuration(250), "250 ms");
  assert.equal(formatBackupDuration(42_000), "42s");
  assert.equal(formatBackupDuration(125_000), "2m 5s");
  assert.equal(formatBackupDuration(3_723_000), "1h 2m");
  assert.equal(backupFileBaseName("/a/b/x_20260101-000000.sql.gz"), "x_20260101-000000.sql.gz");
  assert.equal(backupFileBaseName("C:\\bak\\y.sql"), "y.sql");
});

test("new SQL draft and legacy empty format retain SQL semantics", () => {
  assert.equal(createBackupJobDraft("mysql", "app").format, "sql");
  const old = { ...backupJobFromDraft(validDraft()), format: "" };
  assert.equal(draftFromBackupJob(old).format, "sql");
  assert.equal(draftFromBackupJob({ ...old, format: "archive" }).format, "archive");
});
