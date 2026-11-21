import assert from "node:assert/strict";
import test from "node:test";
import { describePluginInstallError } from "../src/domain/plugins/install-error.ts";

test("keeps unknown messages as-is", () => {
  const info = describePluginInstallError("RuntimeError: cannot read plugin archive");
  assert.equal(info.kind, "unknown");
  assert.equal(info.detail, "cannot read plugin archive");
});
