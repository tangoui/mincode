import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import { URL } from "node:url";
import test from "node:test";
import {
  migrateLegacyProjectWorkspace,
  restoreProjectWorkspaceCollection,
} from "../src/domain/projects/workspace-persistence.ts";

function restoreSnapshot(value) {
  if (!value || typeof value !== "object" || typeof value.projectRoot !== "string") return null;
  return { projectRoot: value.projectRoot };
}

test("restores valid workspace snapshots and drops malformed entries", () => {
  const restored = restoreProjectWorkspaceCollection(
    {
      version: 1,
      workspaces: {
        "workspace:default": { projectRoot: "/repo/min" },
        "workspace:test": { projectRoot: "/repo/test" },
        "": { projectRoot: "/repo/invalid" },
        "workspace:broken": { projectRoot: 42 },
      },
    },
    restoreSnapshot
  );

  assert.deepEqual(restored, {
    version: 1,
    workspaces: {
      "workspace:default": { projectRoot: "/repo/min" },
      "workspace:test": { projectRoot: "/repo/test" },
    },
  });
});

test("migrates the legacy global snapshot only into the active workspace", () => {
  const migrated = migrateLegacyProjectWorkspace(
    null,
    "workspace:test",
    { projectRoot: "/repo/legacy" }
  );

  assert.deepEqual(migrated, {
    version: 1,
    workspaces: {
      "workspace:test": { projectRoot: "/repo/legacy" },
    },
  });
});

test("keeps an existing collection unchanged so migration is idempotent", () => {
  const existing = {
    version: 1,
    workspaces: {
      "workspace:default": { projectRoot: "/repo/current" },
    },
  };

  assert.equal(
    migrateLegacyProjectWorkspace(existing, "workspace:test", { projectRoot: "/repo/legacy" }),
    existing
  );
});

test("switching local projects keeps open documents and unsaved content", async () => {
  const view = await readFile(
    new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url),
    "utf8"
  );
  const openProject = view.match(
    /async function openProject[\s\S]*?\n}\n\nasync function chooseProject/
  )?.[0];
  const activateProject = view.match(
    /async function activateProject[\s\S]*?\n}\n\nasync function openFile/
  )?.[0];

  assert.ok(openProject, "openProject implementation must be present");
  assert.match(openProject, /project\.restoreProject\(normalized\)/);
  assert.doesNotMatch(openProject, /project\.resetProject\(normalized\)/);
  assert.doesNotMatch(openProject, /confirmDiscardWorkspace\(\)/);

  assert.ok(activateProject, "activateProject implementation must be present");
  assert.match(activateProject, /project\.restoreProject\(root\)/);
  assert.doesNotMatch(activateProject, /confirmDiscardWorkspace\(\)/);
});

test("project file clipboard shortcut yields to text selection and inactive cached views", async () => {
  const view = await readFile(
    new URL("../src/views/ProjectWorkspaceView.vue", import.meta.url),
    "utf8"
  );
  // ⌘/Ctrl+C/V are keymap commands (project:copy-node / project:paste-node);
  // HOST_KEYBINDING_RULES keeps `!text.selection` on copy and the
  // `tree.selection` context excludes inputs, Monaco and the terminal.
  const hotkey = view.match(
    /function startProjectClipboardHotkey[\s\S]*?\n}\n\nfunction stopProjectClipboardHotkey[\s\S]*?\n}\n/
  )?.[0];
  const deactivation = [...view.matchAll(/onDeactivated\(\(\) => \{[\s\S]*?\n}\);/g)].map((match) => match[0]).join("\n");

  assert.ok(hotkey, "project clipboard command registration must be present");
  assert.match(hotkey, /registerKeybindingContext\("tree\.selection"/);
  assert.match(hotkey, /input, textarea, \[contenteditable\], \.monaco-editor, \.xterm/);
  assert.match(hotkey, /id: "project:copy-node"/);
  assert.match(hotkey, /for \(const dispose of projectClipboardDisposers\) dispose\(\)/);
  assert.ok(deactivation, "cached-view deactivation hook must be present");
  assert.match(deactivation, /stopProjectClipboardHotkey\(\)/);
  const hostRules = await readFile(new URL("../src/domain/keybindings/presets/mincode.ts", import.meta.url), "utf8");
  assert.match(hostRules, /command: "project:copy-node", when: "tree\.selection && !text\.selection"/);
});
