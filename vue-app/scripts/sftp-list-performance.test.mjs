import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import {
  SFTP_COMPLETED_TRANSFER_LIMIT,
  pruneCompletedSftpTransfers,
  useSftpTransferQueue,
} from "@/composables/useSftpTransferQueue";
import { SFTP_VIRTUAL_ROW_THRESHOLD, computeVirtualRowWindow } from "@/components/sftp/use-virtual-rows";

const read = (path) => readFile(new URL(`../src/${path}`, import.meta.url), "utf8");
const view = await read("views/SftpWorkspaceView.vue");
const fileList = await read("components/sftp/SftpFileList.vue");
const transferPanel = await read("components/sftp/SftpTransferPanel.vue");
const sshPanel = await read("components/ssh/SshSftpPanel.vue");

function draft(name) {
  return {
    sessionId: "s1",
    direction: "upload",
    operation: "upload-file",
    name,
    localPath: `/tmp/${name}`,
    remotePath: `/remote/${name}`,
  };
}

test("pruning keeps the newest completed transfers and every failed/cancelled one", () => {
  const items = [
    { id: "a", status: "completed" },
    { id: "b", status: "failed" },
    { id: "c", status: "completed" },
    { id: "d", status: "cancelled" },
    { id: "e", status: "completed" },
    { id: "f", status: "queued" },
  ];
  assert.deepEqual(pruneCompletedSftpTransfers(items, 1).map((item) => item.id), ["b", "d", "e", "f"]);
  assert.equal(pruneCompletedSftpTransfers(items, 3), items, "no-op pruning must keep the array identity");
  assert.ok(SFTP_COMPLETED_TRANSFER_LIMIT > 0);
});

test("transfer queue coalesces progress per frame and applies the final progress on settle", async () => {
  let observedBeforeFlush;
  const queue = useSftpTransferQueue({
    async execute(item, reportProgress) {
      reportProgress({ bytesTransferred: 10, totalBytes: 100, speedBytesPerSecond: 5 });
      reportProgress({ bytesTransferred: 40, totalBytes: 100, speedBytesPerSecond: 7 });
      observedBeforeFlush = item.bytesTransferred;
      await new Promise((resolve) => setTimeout(resolve, 40));
      reportProgress({ bytesTransferred: 100, totalBytes: 100, speedBytesPerSecond: 9 });
      return { status: "completed" };
    },
  });
  const [created] = queue.enqueue([draft("one.txt")]);
  await new Promise((resolve) => setTimeout(resolve, 80));
  const item = queue.items.value.find((candidate) => candidate.id === created.id);
  assert.equal(observedBeforeFlush, 0, "progress must not be written synchronously");
  assert.equal(item.status, "completed");
  assert.equal(item.bytesTransferred, 100);
  assert.equal(item.speedBytesPerSecond, 0);
  queue.dispose();
});

test("transfer queue auto-prunes completed transfers but keeps failures", async () => {
  const queue = useSftpTransferQueue({
    async execute(item) {
      return item.name.startsWith("bad") ? { status: "failed", error: "boom" } : { status: "completed" };
    },
  });
  const drafts = [draft("bad-0")];
  for (let index = 0; index < SFTP_COMPLETED_TRANSFER_LIMIT + 5; index += 1) drafts.push(draft(`ok-${index}`));
  queue.enqueue(drafts);
  await new Promise((resolve) => setTimeout(resolve, 50));
  const statuses = queue.items.value.map((item) => item.status);
  assert.equal(statuses.filter((status) => status === "completed").length, SFTP_COMPLETED_TRANSFER_LIMIT);
  assert.equal(statuses.filter((status) => status === "failed").length, 1);
  assert.equal(queue.items.value.at(-1).name, `ok-${SFTP_COMPLETED_TRANSFER_LIMIT + 4}`);
  queue.dispose();
});

test("small directories render every row; large ones render a bounded window with spacers", () => {
  const small = computeVirtualRowWindow({ count: SFTP_VIRTUAL_ROW_THRESHOLD, scrollTop: 0, viewportHeight: 300, rowHeight: 30 });
  assert.deepEqual(small, { start: 0, end: SFTP_VIRTUAL_ROW_THRESHOLD, paddingTop: 0, paddingBottom: 0 });

  const large = computeVirtualRowWindow({ count: 10_000, scrollTop: 30_000, viewportHeight: 300, rowHeight: 30 });
  assert.ok(large.start > 0 && large.start <= 1000);
  assert.ok(large.end >= 1010 && large.end - large.start < 100);
  assert.equal(large.paddingTop, large.start * 30);
  assert.equal(large.paddingTop + (large.end - large.start) * 30 + large.paddingBottom, 10_000 * 30);

  const unmeasured = computeVirtualRowWindow({ count: 10_000, scrollTop: 0, viewportHeight: 0, rowHeight: 30 });
  assert.ok(unmeasured.end < 200, "an unmeasured viewport must not mount every row");
});

test("SFTP workspace delegates file and transfer rows to child components", () => {
  assert.doesNotMatch(view, /v-for="item in (entries|localEntries|visibleTransfers)"/);
  assert.match(view, /<SftpFileList[\s\S]*?:entries="localEntries"/);
  assert.match(view, /<SftpFileList[\s\S]*?:entries="entries"/);
  assert.match(view, /<SftpTransferPanel v-if="isReady && transferCount"/);
  assert.match(view, /remoteFileList\.value\?\.scrollToPath\(selected\.value\.path\)/);
  assert.match(fileList, /useVirtualRows\(/);
  assert.match(fileList, /draggable="true"/);
  assert.match(fileList, /@contextmenu\.prevent="emit\('rowContextmenu', \$event, item\)"/);
  assert.match(transferPanel, /<SftpTransferRow[\s\S]*?:item="item"/);
});

test("inline rename input keeps spaces from reaching the row's space-to-select handler", () => {
  assert.match(fileList, /@keydown\.space\.prevent="emit\('select', item\)"/);
  assert.match(fileList, /@keydown\.esc\.stop\.prevent="emit\('cancelRename'\)"\s*@keydown\.space\.stop\s/);
});

test("embedded SSH SFTP panel virtualizes its entry list", () => {
  assert.match(sshPanel, /useVirtualRows\(/);
  assert.match(sshPanel, /v-for="item in visibleEntries"/);
});
