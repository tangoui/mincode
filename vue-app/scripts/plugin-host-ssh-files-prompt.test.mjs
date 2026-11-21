import test from "node:test";
import assert from "node:assert/strict";
import { parsePluginPrompt, promptReply } from "../src/domain/plugins/host-prompts.ts";

const from = { pluginId: "data-backup", pluginName: "Data Backup" };
const details = { connectionId: "srv", connectionName: "Backup server", host: "backup.example", port: 2222, username: "alice", directory: "~/backups/mincode" };
const items = [{ id: "once", label: "once" }, { id: "always", label: "always" }];

test("ssh files approvals carry the connection, host and directory", () => {
  const prompt = parsePluginPrompt({ ...from, id: "prompt-9-9", kind: "sshFiles", title: "Backup server", sshFiles: details, items });
  assert.equal(prompt?.kind, "sshFiles");
  assert.equal(prompt.canRemember, true);
  assert.deepEqual(prompt.sshFiles, details);
  assert.deepEqual(promptReply(prompt, { ids: ["always"] }), { id: "prompt-9-9", reply: { cancelled: false, ids: ["always"] } });
  assert.deepEqual(promptReply(prompt, { ids: ["once"] }), { id: "prompt-9-9", reply: { cancelled: false, ids: ["once"] } });
  assert.equal(promptReply(prompt, { ids: ["forever"] }), undefined);
  assert.equal(promptReply(prompt, { value: "once" }), undefined);
  const onceOnly = parsePluginPrompt({ ...from, id: "prompt-9-10", kind: "sshFiles", title: "", sshFiles: details, items: [items[0]] });
  assert.equal(onceOnly.canRemember, false);
  assert.equal(promptReply(onceOnly, { ids: ["always"] }), undefined);
});

test("malformed ssh files approvals are dropped", () => {
  const base = { ...from, id: "prompt-9-11", kind: "sshFiles", title: "x", items };
  for (const bad of [
    { ...details, connectionId: "" },
    { ...details, host: "" },
    { ...details, port: 0 },
    { ...details, port: "22" },
    { ...details, directory: "relative/dir" },
    { ...details, directory: "/a\nb" },
    undefined,
  ]) assert.equal(parsePluginPrompt({ ...base, sshFiles: bad }), undefined, JSON.stringify(bad));
  assert.equal(parsePluginPrompt({ ...base, sshFiles: details, items: [items[1]] }), undefined);
});
