import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

// The desktop host side (OS file opens, file associations, folder drops) lives in ../main.go.
const [app, project, preview, styles] = await Promise.all([
  readFile(new URL("../src/App.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/components/projects/ExternalFilePreview.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/styles/workspaces/project/workspace.css", import.meta.url), "utf8"),
]);

test("native drops and operating-system file opens route through the app shell", () => {
  assert.match(app, /listenAppEvent<NativeFileDropEvent>\("native-files-dropped"/);
  assert.match(app, /app_consume_opened_files/);
});

test("project editor opens external text, image, PDF, and binary files without copying", () => {
  assert.match(project, /id="project-editor-drop-target"/);
  assert.match(project, /local_fs_inspect_external_file/);
  assert.match(project, /local_fs_read_external_text_file/);
  assert.match(project, /local_fs_read_external_data_url/);
  assert.match(project, /local_fs_write_external_text_file/);
  assert.match(preview, /document\.externalKind === 'pdf'/);
  assert.match(preview, /openSystem/);
  assert.match(styles, /\.project-editor-pane\.file-drop-target-active::before/);
});

test("native folder drops from any workspace open the local-project explorer", () => {
  assert.match(app, /id="app-project-drop-target"[\s\S]*?data-file-drop-target/);
  assert.match(app, /targetId === "app-project-drop-target"[\s\S]*?workbench\.openView\("projects"\)[\s\S]*?enqueueProjectFiles\(normalized\)/);
  assert.match(project, /info\.kind === "directory"[\s\S]*?sideMode\.value = "explorer";[\s\S]*?await openProject\(info\.path\)/);
});

test("hidden cached project views cannot consume a global drop before the active view is ready", () => {
  assert.match(project, /if \(!projectViewReady \|\| !projectViewActive\.value \|\| route\.name !== "projects" \|\| openingPendingProjectFiles\) return/);
  assert.match(project, /onDeactivated\(\(\) => \{ projectViewActive\.value = false; \}\)/);
  assert.match(project, /projectViewReady = true;\s*await openPendingProjectFiles\(\)/);
  assert.match(project, /onActivated\(\(\) => \{\s*projectViewActive\.value = true;\s*void openPendingProjectFiles\(\)/);
});

test("project path copy uses native-aware shared clipboard service", () => {
  const treeCopy = project.slice(project.indexOf("async function copyNodePath("), project.indexOf("async function copyNodeToClipboard("));
  assert.match(treeCopy, /await copyText\(path\)/);
  assert.doesNotMatch(treeCopy, /navigator\.clipboard/);
  assert.match(treeCopy, /errorKey\.value = null/);
  const editorCopy = project.slice(project.indexOf('case "copy-path":'), project.indexOf('case "reveal":', project.indexOf('case "copy-path":')));
  assert.match(editorCopy, /await copyText\(path\)/);
  assert.doesNotMatch(editorCopy, /navigator\.clipboard/);
});


test("startup restores the active file after project initialization, without a sidebar file-read error", () => {
  assert.match(project, /if \(path && projectViewReady\) void ensureDocumentLoaded\(path\)/);
  assert.match(project, /if \(project.activePath\) await ensureDocumentLoaded\(project.activePath\)/);
  const loader = project.slice(project.indexOf('const documentLoader ='), project.indexOf('async function activateProject'));
  assert.match(loader, /await documentLoader.load\(path, projectRoot\)/);
  assert.doesNotMatch(loader, /errorKey.value = "project.readFailed"/);
  assert.match(project, /activeDocument.errorDetail/);
  assert.match(project, /@click="ensureDocumentLoaded\(activeDocument.path\)"/);
});

test("files handed over from outside that lie inside an open project open as project documents", () => {
  const opener = project.slice(project.indexOf("async function openExternalFile("), project.indexOf("const projectViewActive = ref(true);"));
  assert.match(opener, /options: \{ preferProject\?: boolean \}/);
  assert.match(opener, /const root = options\.preferProject \? projectRootContaining\(infoPath\) : undefined;/);
  assert.match(opener, /if \(root && !project\.documents\.some\(\(item\) => item\.path === infoPath && item\.external\)\) \{\s*await openFile\(infoPath, undefined, root\);/);
  assert.ok(opener.indexOf('info.kind === "directory"') < opener.indexOf("options.preferProject"), "directories still open as projects");
  assert.match(project, /for \(const path of consumeProjectFiles\(\)\) await openExternalFile\(path, \{ preferProject: true \}\);/);
  // Go to definition into files outside every project keeps the external path.
  assert.match(project, /openExternalFile\(path\)/);
});
