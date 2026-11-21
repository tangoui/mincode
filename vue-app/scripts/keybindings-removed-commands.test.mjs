import assert from "node:assert/strict";
import test from "node:test";
import { normalizePersistedKeybindings } from "../src/domain/keybindings/persistence.ts";

test("loading old shortcuts ignores removed commands and preserves supported overrides", () => {
  const supported = { command: "workbench:new-terminal", key: "mod+shift+t" };
  const { state } = normalizePersistedKeybindings({
    version: 1,
    overrides: [
      { command: "workbench:show-view.source-control", key: "mod+shift+g" },
      { command: "workbench:show-view.plugins", key: "mod+shift+p" },
      { command: "builtin:lock-app", remove: true },
      supported,
    ],
  });

  assert.deepEqual(state.overrides, [supported]);
});
