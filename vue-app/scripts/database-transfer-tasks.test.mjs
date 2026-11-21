import test from "node:test";
import assert from "node:assert/strict";
import { parseTransferTask, mergeTransferTask, transferTerminal } from "../src/domain/database/transfer-tasks.ts";
const raw = { id: "r", kind: "restore", state: "succeeded", revision: 3, createdAt: 1, updatedAt: 3, result: { records: 7, objects: 1, files: 0, bytes: 0, certainty: "known" } };
test("older event cannot overwrite persisted terminal task", () => {
 const current=parseTransferTask(raw);
 assert.equal(mergeTransferTask(current,parseTransferTask({...raw,state:"running",revision:2})),current);
 assert.equal(transferTerminal(current),true);
});
test("malformed state or counter is rejected", () => {
 assert.throws(()=>parseTransferTask({...raw,state:"ok"}));
 assert.throws(()=>parseTransferTask({...raw,result:{...raw.result,records:-1}}));
 assert.throws(()=>parseTransferTask({...raw,result:{...raw.result,certainty:"rolledback"}}));
});
