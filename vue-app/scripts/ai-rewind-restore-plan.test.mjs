import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const {
  buildRewindRestorePlans,
  groupTurnSnapshotsByRoot,
  overlappingTurnSnapshots,
  planRewindRestore,
  restorablePlans,
  shouldOfferRewindRestore,
} = await import("../src/domain/ai/rewind-restore-plan.ts");
const { combineTurnSnapshotRecords, pruneSnapshotRoot, snapshotKeepTrees } = await import("../src/domain/ai/snapshot-retention.ts");

const ROOT = "/repo";
const tree = (char) => char.repeat(40);
const point = (char, extra = {}) => ({ tree: tree(char), head: "h1", branch: "main", ...extra });

function turn(id, before, after, extra = {}) {
  return {
    userMessageId: id,
    projectRoot: ROOT,
    before: point(before),
    after: point(after),
    status: "captured",
    startedAt: 100,
    finishedAt: 200,
    ...extra,
  };
}

const twoTurns = () => [
  turn("u1", "a", "b", { startedAt: 100, finishedAt: 200 }),
  turn("u2", "c", "d", { startedAt: 300, finishedAt: 400 }),
];
const noDiffs = { between: new Map(), changed: [], overlappingPaths: [] };

test("files changed between turns become conflicts and created files are removed", () => {
  const turns = twoTurns();
  const plan = planRewindRestore(turns, {
    between: new Map([[0, ["user.txt"]]]),
    changed: [
      { path: "a.txt", status: "modified" },
      { path: "new.txt", status: "added" },
      { path: "user.txt", status: "modified" },
    ],
    overlappingPaths: [],
  });
  assert.deepEqual(plan.restore, ["a.txt"]);
  assert.deepEqual(plan.remove, ["new.txt"]);
  assert.deepEqual(plan.conflicts, ["user.txt"]);
  assert.deepEqual(plan.uncovered, []);
  assert.equal(plan.target, turns[0].before.tree);
  assert.equal(plan.expected, turns[1].after.tree);
  assert.equal(plan.projectRoot, ROOT);
  assert.equal(plan.overlapped, false);
  assert.equal(plan.blocked, undefined);
});

test("turns are ordered by time, whatever order the records come in", () => {
  const [first, second] = twoTurns();
  const plan = planRewindRestore([second, first], { ...noDiffs, changed: [{ path: "gone.txt", status: "deleted" }] });
  assert.equal(plan.target, first.before.tree);
  assert.equal(plan.expected, second.after.tree);
  assert.deepEqual(plan.restore, ["gone.txt"]);
});

test("any HEAD or branch change across rewound turns blocks file restore", () => {
  const changed = [{ path: "a.txt", status: "modified" }];
  const headInSecondTurn = twoTurns();
  headInSecondTurn[1].before = point("c", { head: "h2" });
  headInSecondTurn[1].after = point("d", { head: "h2" });
  assert.equal(planRewindRestore(headInSecondTurn, { ...noDiffs, changed }).blocked, "git_state_changed");

  const commitInsideTurn = twoTurns();
  commitInsideTurn[0].after = point("b", { head: "h2" });
  commitInsideTurn[1].before = point("c", { head: "h2" });
  commitInsideTurn[1].after = point("d", { head: "h2" });
  assert.equal(planRewindRestore(commitInsideTurn, { ...noDiffs, changed }).blocked, "git_state_changed");

  const branchSwitch = [turn("u1", "a", "b", { after: point("b", { branch: "feature" }) })];
  const plan = planRewindRestore(branchSwitch, { ...noDiffs, changed });
  assert.equal(plan.blocked, "git_state_changed");
  assert.deepEqual([plan.restore, plan.remove], [[], []]);
});

test("a turn without a snapshot blocks restore, a non-git project reports unsupported", () => {
  const changed = [{ path: "a.txt", status: "modified" }];
  const missing = twoTurns();
  missing[1] = { userMessageId: "u2", projectRoot: ROOT, status: "no_snapshot", startedAt: 300, finishedAt: 400 };
  const plan = planRewindRestore(missing, { ...noDiffs, changed });
  assert.equal(plan.blocked, "no_snapshot");
  assert.deepEqual(plan.restore, []);

  const nonGit = [{ userMessageId: "u1", projectRoot: ROOT, status: "unsupported", startedAt: 1, finishedAt: 2 }];
  assert.equal(planRewindRestore(nonGit, noDiffs).blocked, "unsupported");
  assert.equal(planRewindRestore([...missing, ...nonGit], noDiffs).blocked, "unsupported");
});

test("files another conversation changed during these turns are conflicts and flag the overlap", () => {
  const plan = planRewindRestore(twoTurns(), {
    between: new Map(),
    changed: [{ path: "a.txt", status: "modified" }, { path: "b.txt", status: "modified" }],
    overlappingPaths: ["a.txt"],
  });
  assert.deepEqual(plan.conflicts, ["a.txt"]);
  assert.deepEqual(plan.restore, ["b.txt"]);
  assert.equal(plan.overlapped, true);
});

test("paths inside nested repositories are listed as uncovered and never restored", () => {
  const plan = planRewindRestore(twoTurns(), {
    between: new Map([[0, ["vendor/lib"]]]),
    changed: [{ path: "vendor/lib", status: "submodule" }, { path: "a.txt", status: "modified" }],
    overlappingPaths: [],
  });
  assert.deepEqual(plan.uncovered, ["vendor/lib"]);
  assert.deepEqual(plan.conflicts, []);
  assert.deepEqual(plan.restore, ["a.txt"]);
  assert.deepEqual(plan.remove, []);
});

test("files the size limit left out of either end are uncovered, not restored or removed", () => {
  const turns = twoTurns();
  // grew past 10 MiB during the turn: missing from the after tree, so the diff says "deleted"
  turns[1].after = point("d", { skipped: ["big.bin"] });
  // over the limit before the turn, shrank below it: the diff says "added"
  turns[0].before = point("a", { skipped: ["was-big.bin"] });
  const plan = planRewindRestore(turns, {
    ...noDiffs,
    changed: [
      { path: "big.bin", status: "deleted" },
      { path: "was-big.bin", status: "added" },
      { path: "ok.txt", status: "added" },
    ],
  });
  assert.deepEqual(plan.uncovered, ["big.bin", "was-big.bin"]);
  assert.deepEqual(plan.remove, ["ok.txt"]);
  assert.deepEqual(plan.restore, []);
});

function fakeDiff(table) {
  const calls = [];
  const diff = async (root, from, to) => {
    calls.push([root, from, to]);
    const key = `${root}:${from}:${to}`;
    if (!(key in table)) throw new Error(`unexpected diff ${key}`);
    const value = table[key];
    if (value instanceof Error) throw value;
    return value;
  };
  return { diff, calls };
}

test("plans are built per project root from the backend diffs", async () => {
  const turns = twoTurns();
  const other = { ...turn("x1", "a", "b"), projectRoot: "/other" };
  const { diff, calls } = fakeDiff({
    [`/repo:${tree("a")}:${tree("d")}`]: [{ path: "a.txt", status: "modified" }, { path: "user.txt", status: "modified" }],
    [`/repo:${tree("b")}:${tree("c")}`]: [{ path: "user.txt", status: "modified" }],
    [`/other:${tree("a")}:${tree("b")}`]: [{ path: "o.txt", status: "added" }],
  });
  const plans = await buildRewindRestorePlans({ rewound: [turns[1], other, turns[0]], others: [], diff });
  assert.deepEqual(plans.map((plan) => plan.projectRoot), ["/repo", "/other"]);
  assert.deepEqual(plans[0].restore, ["a.txt"]);
  assert.deepEqual(plans[0].conflicts, ["user.txt"]);
  assert.deepEqual(plans[1].remove, ["o.txt"]);
  assert.equal(calls.length, 3);
  assert.equal(shouldOfferRewindRestore(plans), true);
  assert.deepEqual(restorablePlans(plans).map((plan) => plan.projectRoot), ["/repo", "/other"]);
});

test("a turn's between diff is skipped when nothing happened between turns", async () => {
  const turns = [turn("u1", "a", "b"), turn("u2", "b", "c", { startedAt: 300, finishedAt: 400 })];
  const { diff, calls } = fakeDiff({ [`/repo:${tree("a")}:${tree("c")}`]: [{ path: "a.txt", status: "modified" }] });
  const [plan] = await buildRewindRestorePlans({ rewound: turns, others: [], diff });
  assert.deepEqual(plan.restore, ["a.txt"]);
  assert.equal(calls.length, 1);
});

test("only other conversations' turns on the same root that overlap in time count", () => {
  const rewound = twoTurns(); // 100..400
  const others = [
    turn("o1", "e", "f", { startedAt: 350, finishedAt: 500 }),
    turn("o2", "e", "f", { startedAt: 10, finishedAt: 99 }),
    { ...turn("o3", "e", "f", { startedAt: 150, finishedAt: 160 }), projectRoot: "/elsewhere" },
    turn("o4", "e", "f", { startedAt: 401, finishedAt: 900 }),
    turn("o5", "e", "f", { startedAt: 50, finishedAt: undefined }),
  ];
  assert.deepEqual(overlappingTurnSnapshots(rewound, others).map((item) => item.userMessageId), ["o1", "o5"]);
});

test("files other runs changed are conflicts; a run without a snapshot makes every file a conflict", async () => {
  const rewound = twoTurns();
  const changed = [{ path: "a.txt", status: "modified" }, { path: "b.txt", status: "added" }];
  const table = {
    [`/repo:${tree("a")}:${tree("d")}`]: changed,
    [`/repo:${tree("b")}:${tree("c")}`]: [],
    [`/repo:${tree("e")}:${tree("f")}`]: [{ path: "a.txt", status: "modified" }],
  };
  const [overlap] = await buildRewindRestorePlans({
    rewound,
    others: [turn("o1", "e", "f", { startedAt: 150, finishedAt: 250 })],
    diff: fakeDiff(table).diff,
  });
  assert.equal(overlap.overlapped, true);
  assert.deepEqual(overlap.conflicts, ["a.txt"]);
  assert.deepEqual(overlap.remove, ["b.txt"]);

  const [unknown] = await buildRewindRestorePlans({
    rewound,
    others: [{ userMessageId: "o2", projectRoot: ROOT, status: "no_snapshot", startedAt: 150, finishedAt: 250 }],
    diff: fakeDiff(table).diff,
  });
  assert.equal(unknown.overlapped, true);
  assert.deepEqual(unknown.conflicts, ["a.txt", "b.txt"]);
  assert.deepEqual([unknown.restore, unknown.remove], [[], []]);
});

test("a failing diff blocks that root instead of guessing", async () => {
  const unsupported = Object.assign(new Error("ai_snapshot_unsupported:browser"), { name: "SnapshotUnsupportedError" });
  const [browser] = await buildRewindRestorePlans({
    rewound: [turn("u1", "a", "b")],
    others: [],
    diff: async () => { throw unsupported; },
  });
  assert.equal(browser.blocked, "unsupported");
  const [missing] = await buildRewindRestorePlans({
    rewound: [turn("u1", "a", "b")],
    others: [],
    diff: async () => { throw new Error("snapshot_invalid_tree"); },
  });
  assert.equal(missing.blocked, "no_snapshot");
});

test("the dialog is offered only when there is something to show", async () => {
  const empty = await buildRewindRestorePlans({ rewound: [turn("u1", "a", "a")], others: [], diff: async () => [] });
  assert.equal(shouldOfferRewindRestore(empty), false);
  const blocked = await buildRewindRestorePlans({
    rewound: [{ userMessageId: "u1", projectRoot: ROOT, status: "no_snapshot", startedAt: 1 }],
    others: [],
    diff: async () => assert.fail("no diff for a blocked root"),
  });
  assert.equal(shouldOfferRewindRestore(blocked), true);
  assert.deepEqual(restorablePlans(blocked), []);
  assert.deepEqual(groupTurnSnapshotsByRoot([]), []);
});

// --- retention ---------------------------------------------------------------

const retained = (conversationId, record) => ({ conversationId, record });

test("prune keeps the trees every remaining record on that root still references", async () => {
  const calls = [];
  const records = [
    retained("c1", turn("u1", "a", "b")),
    retained("c2", turn("u2", "b", "c")),
    retained("c3", { ...turn("u3", "e", "f"), projectRoot: "/other" }),
    retained("c4", { userMessageId: "u4", projectRoot: ROOT, status: "no_snapshot", before: { tree: "not-an-id", head: "", branch: "" } }),
  ];
  const result = await pruneSnapshotRoot({
    projectRoot: ROOT,
    records,
    maxBytes: 1000,
    prune: async (root, keep) => { calls.push([root, keep]); return 10; },
  });
  assert.deepEqual(calls, [[ROOT, [tree("a"), tree("b"), tree("c")]]]);
  assert.deepEqual(result, { bytes: 10, evicted: [] });
});

test("prune keeps trees from both the stored and the in-memory copy of a conversation", () => {
  // another window stored turn u2 (tree 2) after this window loaded conversation X (tree 1 only)
  const stored = [
    { conversationId: "X", records: [turn("u1", "1", "1"), turn("u2", "2", "2")] },
    { conversationId: "Z", records: [turn("z1", "5", "5")] },
  ];
  const memory = [
    { conversationId: "X", records: [turn("u1", "1", "1"), turn("u3", "3", "3")] },
    { conversationId: "M", records: [turn("m1", "4", "4")] },
  ];
  const union = combineTurnSnapshotRecords(stored, memory, { union: true });
  assert.deepEqual(snapshotKeepTrees(union.map((item) => item.record)), [tree("1"), tree("2"), tree("3"), tree("4"), tree("5")]);
  assert.equal(union.filter((item) => item.record.userMessageId === "u1").length, 1, "identical copies are not duplicated");
  // overlap detection: memory replaces the stored copy (it holds unsaved rewinds)
  const latest = combineTurnSnapshotRecords(stored, memory, { excludeConversationId: "Z" });
  assert.deepEqual(latest.map((item) => item.record.userMessageId), ["u1", "u3", "m1"]);
});

test("over the size cap the oldest turns lose their snapshots first", async () => {
  const records = [
    retained("c1", turn("new", "d", "e", { startedAt: 400 })),
    retained("c2", turn("old", "a", "b", { startedAt: 100 })),
    retained("c1", turn("mid", "b", "c", { startedAt: 200 })),
    retained("c2", turn("later", "c", "d", { startedAt: 300 })),
  ];
  const sizes = [5000, 5000, 900];
  const calls = [];
  const marked = [];
  const result = await pruneSnapshotRoot({
    projectRoot: ROOT,
    records,
    maxBytes: 1000,
    onEvict: (items) => marked.push({ ids: items.map((item) => item.record.userMessageId), prunesSoFar: calls.length }),
    prune: async (_root, keep) => { calls.push(keep); return sizes.shift(); },
  });
  // records are marked before the store loses their trees
  assert.deepEqual(marked, [{ ids: ["old"], prunesSoFar: 1 }, { ids: ["mid", "later"], prunesSoFar: 2 }]);
  // one turn, then two: the batch doubles so a huge history needs few gc runs
  assert.deepEqual(result.evicted.map((item) => item.record.userMessageId), ["old", "mid", "later"]);
  assert.equal(result.bytes, 900);
  assert.deepEqual(calls, [
    [tree("a"), tree("b"), tree("c"), tree("d"), tree("e")],
    [tree("b"), tree("c"), tree("d"), tree("e")],
    [tree("d"), tree("e")],
  ]);
});

test("the size loop stops once nothing is left to drop, even above the cap", async () => {
  const records = Array.from({ length: 9 }, (_, index) => retained("c", turn(`u${index}`, "123456789"[index], "123456789"[index], { startedAt: index })));
  let calls = 0;
  const result = await pruneSnapshotRoot({
    projectRoot: ROOT,
    records,
    maxBytes: 1000,
    prune: async () => { calls += 1; return 5000; },
  });
  assert.equal(result.evicted.length, 9);
  assert.equal(result.bytes, 5000);
  // doubling batches: 1 + (1, 2, 4, 2) evictions -> 5 prunes, the last with an empty keep
  assert.ok(calls <= 6, `pruned ${calls} times`);
});

// --- wiring ------------------------------------------------------------------

const drawer = readFileSync(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
const store = readFileSync(new URL("../src/stores/ai-assistant.ts", import.meta.url), "utf8");

test("the rewound turns' records are read before the conversation is rewound", () => {
  const body = drawer.slice(drawer.indexOf("async function resendFromMessage"));
  const ask = body.indexOf("await prepareRewind(");
  const rewind = body.indexOf("assistant.rewindConversationTo(");
  assert.ok(ask > 0 && rewind > ask, "the restore decision comes before rewindConversationTo");
  const prepare = drawer.slice(drawer.indexOf("async function prepareRewind"), drawer.indexOf("async function resendFromMessage"));
  assert.ok(prepare.indexOf("await confirmRewindRestore(") < prepare.indexOf("await restoreRewoundFiles("));
  const confirm = drawer.slice(drawer.indexOf("async function confirmRewindRestore"), drawer.indexOf("function settleRewindRestore"));
  assert.match(confirm, /rewoundTurnSnapshots\(conversationId, messageId\)/);
  assert.match(drawer.slice(drawer.indexOf("function rewoundTurnSnapshots")), /conversation\.turnSnapshots/);
  for (const part of [prepare, confirm]) assert.doesNotMatch(part, /rewindConversationTo/);
  // cancelling the dialog keeps everything, including an edit in progress
  assert.match(prepare, /if \(choice === "cancel"\) \{\s*onCancel\?\.\(\);\s*return false;/);
  // every other early return puts the edit back too
  const returns = [...prepare.matchAll(/return false;/g)].length;
  const restored = [...prepare.matchAll(/onCancel\?\.\(\);(?:\s*ElMessage\.warning\([^;]+;)?\s*return false;/g)].length;
  assert.equal(returns, 5, "the guard, cancel, conversation changed, failed restore, changed after restore");
  assert.equal(restored, 4, "only the re-entry guard returns without restoring the edit");
  assert.match(prepare, /if \(!rewindable\(\)\) \{\s*onCancel\?\.\(\);\s*ElMessage\.warning\(t\("ai\.rewindRestore\.conversationChanged"\)\);\s*return false;/);
});

test("a failed restore asks before the conversation is rewound", () => {
  const restore = drawer.slice(drawer.indexOf("async function restoreRewoundFiles"), drawer.indexOf("async function prepareRewind"));
  assert.match(restore, /catch \(error\) \{\s*try \{\s*await ElMessageBox\.confirm\(\s*t\("ai\.rewindRestore\.failed"/);
  // keeping the conversation after a failed restore stops the rewind
  assert.match(restore, /\} catch \{\s*return false;\s*\}/);
  const prepare = drawer.slice(drawer.indexOf("async function prepareRewind"), drawer.indexOf("async function resendFromMessage"));
  assert.match(prepare, /if \(!\(await restoreRewoundFiles\(plans\)\)\) \{\s*onCancel\?\.\(\);\s*return false;/);
});

test("deleting a conversation prunes its roots, never while a turn may be in flight", () => {
  const remove = store.slice(store.indexOf("async function deleteConversation"), store.indexOf("function clearConversationMessages"));
  assert.match(remove, /scheduleSnapshotPrune\(/);
  const schedule = store.slice(store.indexOf("function scheduleSnapshotPrune"));
  assert.match(schedule.slice(0, 600), /runningConversationIdsKey\.value/);
  // a turn that starts while the keep set is computed defers the root again
  assert.match(store, /if \(runningConversationIdsKey\.value\) throw new SnapshotPruneDeferred\(\);\s*return pruneProjectSnapshots\(projectRoot, keep\);/);
  assert.match(store, /if \(error instanceof SnapshotPruneDeferred\) pendingSnapshotPruneRoots\.add\(root\);/);
  // retried when the last run ends
  assert.match(store, /watch\(runningConversationIdsKey, \(runningKey\) => \{\s*if \(!runningKey\) scheduleSnapshotPrune\(\[\]\);/);
  // keep comes from every conversation, stored or in memory, and a failed listing prunes nothing
  const run = store.slice(store.indexOf("async function runPendingSnapshotPrunes"), store.indexOf("function scheduleSnapshotPrune"));
  assert.match(run, /records = await collectTurnSnapshotRecords\(\{ union: true \}\);\s*\} catch \{\s*return;/);
  assert.match(store, /return combineTurnSnapshotRecords\(stored, memory, options\);/);
  assert.match(run, /onEvict: markTurnSnapshotsEvicted/);
});

test("a turn still running on the same root makes every candidate file a conflict", async () => {
  const { untrackedRunTurnSnapshots } = await import("../src/domain/ai/rewind-restore-plan.ts");
  const rewound = [turn("u1", "a", "b", { startedAt: 100, finishedAt: 200 })];
  const diff = async () => [{ path: "x.txt", status: "modified" }, { path: "new.txt", status: "added" }];
  // in flight in this window: started during the rewound turn, no after snapshot yet
  const inFlight = { userMessageId: "o1", projectRoot: ROOT, status: "no_snapshot", startedAt: 150, before: point("e") };
  let [plan] = await buildRewindRestorePlans({ rewound, others: [inFlight], diff });
  assert.equal(plan.overlapped, true);
  assert.deepEqual(plan.restore, []);
  assert.deepEqual(plan.remove, []);
  assert.deepEqual(plan.conflicts.sort(), ["new.txt", "x.txt"]);
  assert.deepEqual(restorablePlans([plan]), []);
  assert.equal(shouldOfferRewindRestore([plan]), true, "the dialog explains the overlap");

  // running in another window: only the run's start and its conversation's bound root are known
  const untracked = untrackedRunTurnSnapshots([ROOT], [
    { projectRoot: `${ROOT}/`, startedAt: 150 },
    { projectRoot: "/elsewhere", startedAt: 150 },
    { startedAt: 150 },
    { projectRoot: `${ROOT}/sub`, startedAt: 150 },
    { projectRoot: "/", startedAt: 150 },
  ]);
  assert.equal(untracked.length, 4, "same, unknown, nested and enclosing roots count; a different root does not");
  for (const record of untracked) {
    assert.equal(record.projectRoot, ROOT);
    assert.equal(record.after, undefined);
  }
  [plan] = await buildRewindRestorePlans({ rewound, others: untracked, diff });
  assert.equal(plan.overlapped, true);
  assert.deepEqual(plan.restore, []);
  assert.deepEqual(plan.conflicts.sort(), ["new.txt", "x.txt"]);

  // a run without a known start overlaps any span; one that started after the turns does not
  const [unknownStart] = untrackedRunTurnSnapshots([ROOT], [{ projectRoot: ROOT }]);
  assert.equal(overlappingTurnSnapshots(rewound, [unknownStart]).length, 1);
  const [later] = untrackedRunTurnSnapshots([ROOT], [{ projectRoot: ROOT, startedAt: 500 }]);
  assert.equal(overlappingTurnSnapshots(rewound, [later]).length, 0);
});

test("the restore dialog counts runs still in flight, here and in other windows", () => {
  const confirm = drawer.slice(drawer.indexOf("async function confirmRewindRestore"), drawer.indexOf("function settleRewindRestore"));
  assert.match(confirm, /others: \[\.\.\.others\.map\(\(item\) => item\.record\), \.\.\.runningTurnSnapshots\(conversationId, rewound\)\]/);
  const running = drawer.slice(drawer.indexOf("function runningTurnSnapshots"), drawer.indexOf("async function confirmRewindRestore"));
  assert.match(running, /turnSnapshotRecorder\.inFlight\(\)/);
  assert.match(running, /progress\.activeRuns/);
  // conversations this window runs agentic turns for are covered by the recorder
  assert.match(running, /activeSnapshotTurns\.has\(run\.navigation\.conversationId\)/);
  assert.match(running, /untrackedRunTurnSnapshots\(/);
  assert.match(running, /startedAt: run\.progress\.createdAt/);
});

test("resending an edit puts it back whenever the rewind does not happen", () => {
  const resend = drawer.slice(drawer.indexOf("async function resendFromMessage"), drawer.indexOf("function resendEditedMessage"));
  // the conversation started running (or left the editable state) before the resend
  assert.match(resend, /if \(!canRewindActiveConversation\.value\) \{\s*onCancel\?\.\(\);\s*if \(onCancel\) ElMessage\.warning\(t\("ai\.rewindRestore\.conversationChanged"\)\);\s*return;\s*\}/);
  // rewindConversationTo refused (running again, message gone): files may have been restored already
  assert.match(resend, /if \(!removed\) \{\s*onCancel\?\.\(\);\s*ElMessage\.warning\(t\(rewindRestoredFiles \? "ai\.rewindRestore\.notRewound" : "ai\.rewindRestore\.conversationChanged"\)\);\s*return;\s*\}/);
  const prepare = drawer.slice(drawer.indexOf("async function prepareRewind"), drawer.indexOf("async function resendFromMessage"));
  assert.match(prepare, /rewindPreparing = true;\s*rewindRestoredFiles = false;/);
  assert.match(prepare, /if \(!\(await restoreRewoundFiles\(plans\)\)\) \{[\s\S]*?\}\s*rewindRestoredFiles = true;/);
  const bare = [...resend.matchAll(/\) return;/g)].map((match) => resend.slice(Math.max(0, match.index - 80), match.index + 9));
  assert.deepEqual(bare.filter((line) => !/prepareRewind/.test(line)), [], "prepareRewind restores the edit itself; no other bare return");
});
