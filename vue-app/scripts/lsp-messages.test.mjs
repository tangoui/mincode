import assert from "node:assert/strict";
import test from "node:test";
import {
  LSP_COMMAND_FEEDBACK_MS,
  LSP_MESSAGE_MAX_CHARS,
  lspCommandFailureText,
  lspServerShortName,
  lspShowMessageToast,
} from "../src/domain/projects/lsp-messages.ts";

test("errors and warnings from window/showMessage are always shown", () => {
  assert.deepEqual(lspShowMessageToast({ type: 1, message: "build failed" }, undefined), { kind: "error", message: "build failed" });
  assert.deepEqual(lspShowMessageToast({ type: 2, message: "go.work is stale" }, undefined), { kind: "warning", message: "go.work is stale" });
});

test("info messages are shown only shortly after the user ran a server command", () => {
  // gopls: "Finished loading packages." on every start must stay quiet.
  assert.equal(lspShowMessageToast({ type: 3, message: "Finished loading packages." }, undefined), null);
  assert.deepEqual(lspShowMessageToast({ type: 3, message: "completed" }, 1200), { kind: "info", message: "completed" });
  assert.equal(lspShowMessageToast({ type: 3, message: "completed" }, LSP_COMMAND_FEEDBACK_MS + 1), null);
});

test("log-level and malformed messages are never shown", () => {
  assert.equal(lspShowMessageToast({ type: 4, message: "Running..." }, 10), null);
  assert.equal(lspShowMessageToast({ type: 5, message: "debug" }, 10), null);
  assert.equal(lspShowMessageToast({ type: 1, message: "   " }, 10), null);
  assert.equal(lspShowMessageToast({ type: 1 }, 10), null);
  assert.equal(lspShowMessageToast(null, 10), null);
});

test("long messages are collapsed to one line and truncated", () => {
  const toast = lspShowMessageToast({ type: 1, message: `line one\n\tline two ${"x".repeat(1000)}` }, undefined);
  assert.equal(toast.message.includes("\n"), false);
  assert.ok(toast.message.startsWith("line one line two"));
  assert.equal(toast.message.length, LSP_MESSAGE_MAX_CHARS);
  assert.ok(toast.message.endsWith("…"));
});

test("command failures name the server reason in one bounded line", () => {
  assert.equal(lspCommandFailureText(new Error("exit status 1:\nmissing touch")), "exit status 1: missing touch");
  assert.equal(lspCommandFailureText("plain"), "plain");
  assert.equal(lspCommandFailureText(undefined), "");
  assert.equal(lspCommandFailureText(new Error("y".repeat(1000))).length, LSP_MESSAGE_MAX_CHARS);
});

test("server names drop the plugin prefix", () => {
  assert.equal(lspServerShortName("gopls"), "gopls");
  assert.equal(lspServerShortName("plugin:dev.codelens-lab/lab"), "lab");
});
