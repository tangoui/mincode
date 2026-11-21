import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

const projectViewUrl = new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url);

async function projectViewSource() {
  return readFile(projectViewUrl, "utf8");
}

test("project command actions follow KeepAlive activation lifecycle", async () => {
  const source = await projectViewSource();

  assert.match(source, /import \{[^}]*\bonActivated\b[^}]*\bonDeactivated\b[^}]*\} from "vue";/s);
  assert.match(
    source,
    /function registerAiProjectWorkspaceActions\(\)[\s\S]*?registerAiWorkspaceActionHandler\(\s*"projects",\s*handleAiProjectWorkspaceAction\s*\)/,
  );
  assert.match(
    source,
    /registerAiProjectWorkspaceActions\(\);\s*onActivated\(\(\) => \{\s*registerAiProjectWorkspaceActions\(\);\s*startProjectClipboardHotkey\(\);\s*\}\);/,
  );
  assert.match(
    source,
    /onDeactivated\(\(\) => \{\s*unregisterAiProjectWorkspaceActions\?\.\(\);\s*unregisterAiProjectWorkspaceActions = undefined;\s*stopProjectClipboardHotkey\(\);\s*\}\);/,
  );
});
