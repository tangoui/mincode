import assert from "node:assert/strict";
import test from "node:test";
import { requiresPluginApproval } from "../src/domain/plugin-ui/approval-policy.ts";

test("plugin read and write actions are approved by default", () => {
  assert.equal(requiresPluginApproval("read"), false);
  assert.equal(requiresPluginApproval("write"), false);
});

test("dangerous plugin actions still require confirmation", () => {
  assert.equal(requiresPluginApproval("dangerous"), true);
});
