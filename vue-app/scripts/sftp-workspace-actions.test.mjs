import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const view = await readFile(new URL("../src/views/SftpWorkspaceView.vue", import.meta.url), "utf8");
const panel = await readFile(new URL("../src/components/ssh/SshSftpPanel.vue", import.meta.url), "utf8");

test("remote SFTP context menu exposes safe directory and entry actions", () => {
  assert.match(view, /@contextmenu\.prevent="onRemotePaneContextMenu"/);
  assert.match(view, /t\("sftp\.newFolder"\)/);
  assert.match(view, /t\("sftp\.newFile"\)/);
  assert.match(view, /remoteContextMenu\.item\.isDir \? t\("sftp\.openFolder"\) : t\("sftp\.preview"\)/);
  assert.match(view, /t\("sftp\.deleteConfirm"\)\.replace\("\{name\}", selected\.value\.name\)/);
});

test("remote SFTP list formats metadata and preserves a valid selection on refresh", () => {
  assert.match(view, /formatModified\(item\.modified\)/);
  assert.match(view, /formatEntrySize\(item\)/);
  assert.match(view, /entries\.value\.find\(\(item\) => item\.path === selectedPath\)/);
});


test("remote and local listings report errors, restore the shown directory and ignore stale responses", () => {
  assert.match(view, /if \(!isCurrent\(\)\) return false;\s*if \(remotePath\.value === path\) remotePath\.value = listedRemotePath;\s*throw error;/);
  assert.match(view, /async function listRemote\(\) \{\s*try \{\s*return await loadRemoteEntries\(\);\s*\} catch \(error\) \{\s*ElMessage\.error\(String\(error\)\);/);
  assert.match(view, /if \(sequence !== localListSequence\) return false;\s*if \(localRoot\.value === root && localPath\.value === path\) \{\s*localRoot\.value = listedLocal\.root;\s*localPath\.value = listedLocal\.path;/);
  assert.match(view, /async function listLocal\(\) \{\s*try \{\s*return await loadLocalEntries\(\);\s*\} catch \(error\) \{\s*ElMessage\.error\(String\(error\)\);/);
});

test("remote editor save reports failures and only clears dirty for the saved snapshot", () => {
  assert.match(view, /if \(!tab \|\| tab\.isImage \|\| tab\.saving\) return false;\s*const savedContent = tab\.content;\s*tab\.saving = true;/);
  assert.match(view, /await writeSftpTextFile\(sessionId\.value, tab\.path, savedContent\);\s*\} catch \(error\) \{\s*ElMessage\.error\(String\(error\)\);\s*return false;\s*\} finally \{\s*tab\.saving = false;\s*\}/);
  assert.match(view, /if \(tab\.content === savedContent\) tab\.dirty = false;/);
  assert.doesNotMatch(view, /\}\s*tab\.dirty = false;/);
  assert.match(panel, /target\.saving\) return;\s*const savedContent = target\.content;/);
  assert.match(panel, /if \(target\.content === savedContent\) target\.dirty = false;/);
  assert.match(panel, /finally \{\s*target\.saving = false;/);
});

test("user-initiated disconnects confirm before discarding unsaved remote edits", () => {
  assert.match(view, /class="sftp-disconnect-button" @click="requestDisconnect"/);
  assert.match(view, /:model-value="profileId"[\s\S]*?@update:model-value="selectProfile"/);
  assert.doesNotMatch(view, /v-model="profileId"/);
  assert.match(view, /if \(!\(await requestDisconnect\(\)\)\) return;\s*profileId\.value = nextProfileId;/);
  assert.match(view, /if \(sessionId\.value && !\(await requestDisconnect\(\)\)\) return;\s*profileId\.value = value;/);
  assert.match(view, /t\("sftp\.unsavedDisconnectConfirm"\)/);
});

test("embedded SSH SFTP panel ignores stale listings and reports non-cancel dialog failures", () => {
  assert.match(panel, /if \(sequence !== listSequence\) return;/);
  assert.match(panel, /finally \{\s*if \(sequence === listSequence\) loading\.value = false;/);
  const cancelChecks = panel.match(/if \(cause === "cancel" \|\| cause === "close"\) return;\s*ElMessage\.error\(String\(cause\)\);/g) ?? [];
  assert.ok(cancelChecks.length >= 2);
  assert.doesNotMatch(panel, /catch \{\s*\/\/ 用户取消。/);
});

test("embedded SSH SFTP panel confirms deletion and validates names", () => {
  assert.match(panel, /isSafeSftpEntryName/);
  assert.match(panel, /t\("sftp\.deleteConfirm"\)\.replace\("\{name\}", selected\.value\.name\)/);
});
