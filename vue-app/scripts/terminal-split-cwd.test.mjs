import assert from "node:assert/strict";
import test from "node:test";
import { cloneTerminalPaneTree, inheritSplitCwd } from "../src/domain/terminal/types.ts";

const localPane = {
  id: "terminal-1",
  type: "local",
  shellType: "system",
  cwd: "/home/user/projects/min",
};

test("inherits the source pane working directory when splitting", () => {
  assert.deepEqual(inheritSplitCwd({}, localPane), {
    initialCwd: "/home/user/projects/min",
  });
  assert.deepEqual(inheritSplitCwd({ shellType: "zsh" }, localPane), {
    shellType: "zsh",
    initialCwd: "/home/user/projects/min",
  });
});

test("keeps an explicitly provided initial working directory", () => {
  assert.deepEqual(
    inheritSplitCwd({ initialCwd: "/tmp" }, localPane),
    { initialCwd: "/tmp" }
  );
});

test("does not inherit for ssh panes or missing cwd", () => {
  assert.deepEqual(inheritSplitCwd({}, { id: "ssh-1", type: "ssh", profile: {} }), {});
  assert.deepEqual(inheritSplitCwd({}, { ...localPane, cwd: "  " }), {});
  assert.deepEqual(inheritSplitCwd({}, undefined), {});
});
