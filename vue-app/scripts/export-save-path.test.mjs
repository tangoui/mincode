import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { parsePluginHostSaveTextResult } from "../src/domain/plugin-ui/host-result.mjs";

const downloadService = await readFile(
  new URL("../src/services/file-download.ts", import.meta.url),
  "utf8",
);
const databaseView = await readFile(
  new URL("../src/views/DatabaseWorkspaceView.vue", import.meta.url),
  "utf8",
);
const sshView = await readFile(
  new URL("../src/views/SshWorkspaceView.vue", import.meta.url),
  "utf8",
);
const settingsView = await readFile(
  new URL("../src/views/SettingsWorkspaceView.vue", import.meta.url),
  "utf8",
);
const pluginFrame = await readFile(
  new URL("../src/components/plugins/PluginViewFrame.vue", import.meta.url),
  "utf8",
);

test("saveTextFileToDisk uses the native save dialog in the desktop runtime", () => {
  // WKWebView 不支持 <a download>，桌面端必须走原生对话框 + 后端写盘。
  assert.match(downloadService, /if \(isDesktopRuntime\(\)\)/);
  assert.match(downloadService, /chooseNativeSaveFile\(title, filename, filters\)/);
  assert.match(downloadService, /local_fs_write_selected_text_file/);
  assert.match(downloadService, /if \(!path\) return false;/);
});

test("saveBinaryFileToDisk writes decoded bytes through the desktop backend", () => {
  // xlsx 等二进制导出以后端 base64 返回，桌面端必须走二进制写盘命令。
  assert.match(downloadService, /local_fs_write_selected_binary_file/);
  assert.match(downloadService, /Uint8Array\.from\(atob\(contentBase64\)/);
});

test("export entry points save through shared save helpers instead of anchor downloads", () => {
  assert.match(databaseView, /saveExportedTableData\(/);
  assert.match(databaseView, /title: t\("database\.exportTableData"\)/);
  assert.match(databaseView, /saveBinaryFileToDisk\(\{ \.\.\.options, contentBase64: content \}\)/);
  assert.match(databaseView, /saveTextFileToDisk\(\{ \.\.\.options, content \}\)/);
  assert.doesNotMatch(databaseView, /function downloadText\(/);
  assert.match(sshView, /title: t\("ssh\.manager\.batchCommandExport"\)/);
  assert.match(sshView, /filename: "mincode-ssh-connections\.json"/);
  assert.match(settingsView, /title: t\("settings\.data\.export"\)/);
});

test("no desktop export path relies on raw anchor downloads", () => {
  // 允许浏览器回退分支保留锚点下载，但各视图不得再直接构造锚点下载。
  assert.doesNotMatch(sshView, /URL\.createObjectURL/);
  assert.doesNotMatch(settingsView, /anchor\.download\s*=/);
});

test("plugin host file handling is generic and rejects unsafe filenames", () => {
  const result = {
    type: "save-text-file",
    file: { filename: "result.txt", content: "safe" },
    savedResult: { saved: true },
    cancelledResult: { saved: false },
  };
  assert.deepEqual(parsePluginHostSaveTextResult(result), {
    filename: "result.txt",
    content: "safe",
    savedResult: { saved: true },
    cancelledResult: { saved: false },
  });
  for (const filename of ["../result.txt", "dir/result.txt", "dir\\result.txt", "bad\u0000.txt"]) {
    assert.throws(() => parsePluginHostSaveTextResult({ ...result, file: { ...result.file, filename } }));
  }
  assert.match(pluginFrame, /action\.resultHandling === "save-text-file"/);
  assert.doesNotMatch(pluginFrame, /certificate-manager|acmeGenerateCSR|acmeCertificateExport/);
});
