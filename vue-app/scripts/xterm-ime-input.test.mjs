import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import test from "node:test";
import { setTimeout as delay } from "node:timers/promises";

// Exercises the patched xterm CompositionHelper (patches/@xterm__xterm@6.0.0.patch) straight from
// the bundle Vite serves: keys typed while a macOS IME is active arrive as keyCode 229 and must
// reach the PTY exactly once and in order, even when the user types faster than the checks run.
const require = createRequire(import.meta.url);
const bundle = readFileSync(require.resolve("@xterm/xterm/lib/xterm.mjs"), "utf8");
const start = bundle.indexOf("compositionstart(){");
const end = bundle.search(/updateCompositionElements\([a-z]\)\{if/);
assert.ok(start > 0 && end > start, "CompositionHelper methods not found in xterm bundle");

const CompositionHelper = new Function(
  "b",
  `return class {
    constructor(textarea, coreService) {
      this._textarea = textarea;
      this._compositionView = { textContent: "", classList: { add() {}, remove() {} } };
      this._coreService = coreService;
      this._isComposing = false;
      this._isSendingComposition = false;
      this._compositionPosition = { start: 0, end: 0 };
      this._dataAlreadySent = "";
      this._pendingTextareaChecks = 0;
      this.updateCompositionElements = () => {};
    }
    ${bundle.slice(start, end)}
  }`,
)({ DEL: "\x7f" });

const tick = () => delay(0);

function createImeTerminal() {
  const textarea = { value: "" };
  const sent = [];
  const helper = new CompositionHelper(textarea, { triggerDataEvent: (data) => sent.push(data) });
  return {
    helper,
    sent,
    imeKey(text) {
      assert.equal(helper.keydown({ keyCode: 229 }), false);
      textarea.value += text;
    },
    enter() {
      if (helper.keydown({ keyCode: 13 })) {
        textarea.value = "";
        sent.push("\r");
      }
    },
    async settle() {
      await tick();
      await tick();
      return sent.join("");
    },
  };
}

test("IME keys typed one at a time are sent once each", async () => {
  const term = createImeTerminal();
  term.imeKey("l");
  await tick();
  term.imeKey("s");
  assert.equal(await term.settle(), "ls");
});

test("IME keys typed faster than the post-keydown check are not dropped or duplicated", async () => {
  const term = createImeTerminal();
  term.imeKey("a");
  term.imeKey("b");
  term.imeKey("c");
  assert.equal(await term.settle(), "abc");
});

test("Enter right after fast IME keys keeps the typed text before the carriage return", async () => {
  const term = createImeTerminal();
  term.imeKey("l");
  term.imeKey("s");
  term.enter();
  assert.equal(await term.settle(), "ls\r");
});

test("text typed right before a composition starts is sent before the composition", async () => {
  const term = createImeTerminal();
  term.imeKey("x");
  term.helper.compositionstart();
  assert.equal(await term.settle(), "x");
});
