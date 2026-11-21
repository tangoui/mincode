import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import test from "node:test";
import { setTimeout as delay } from "node:timers/promises";
import { Window } from "happy-dom";

const require = createRequire(import.meta.url);
const bundle = readFileSync(require.resolve("@xterm/xterm/lib/xterm.mjs"), "utf8");

function openTerminal(context, options = {}) {
  const window = new Window();
  const navigator = { platform: "MacIntel", userAgent: "Version/26.0 Safari/605.1.15", language: "en" };
  const load = new Function("window", "document", "navigator", "process", bundle.replace(/export\{(\w+) as Terminal\};?/, "return $1;"));
  const Terminal = load(window, window.document, navigator, undefined);
  const term = new Terminal(options);
  term.open(window.document.body);
  context.after(async () => {
    term.dispose();
    await window.happyDOM.close();
  });

  const sent = [];
  term.onData((data) => sent.push(data));
  const area = term.textarea;
  return {
    sent,
    key(type, key, keyCode) {
      const event = new window.KeyboardEvent(type, { key, keyCode, bubbles: true, cancelable: true });
      area.dispatchEvent(event);
      return event.defaultPrevented;
    },
    insert(data, inputType = "insertText", isComposing = false) {
      area.value += data;
      area.dispatchEvent(new window.InputEvent("input", { data, inputType, isComposing, composed: true, bubbles: true }));
    },
    composition(type, data) {
      area.dispatchEvent(new window.CompositionEvent(type, { data, bubbles: true }));
    },
  };
}

test("macOS fast typing sends insertText before keydown 229 even while the previous key is held", async (context) => {
  const term = openTerminal(context);
  term.insert("c");
  term.key("keydown", "c", 229);
  await delay(5);
  term.insert("d");
  term.key("keydown", "d", 229);
  term.key("keyup", "c", 67);
  term.key("keyup", "d", 68);
  await delay(5);
  assert.equal(term.sent.join(""), "cd");
});

test("macOS rapid alternating input reaches the terminal in full before Enter", async (context) => {
  const term = openTerminal(context);
  const expected = "cd".repeat(64);
  for (let offset = 0; offset < expected.length; offset += 2) {
    term.insert("c");
    term.key("keydown", "c", 229);
    term.insert("d");
    term.key("keydown", "d", 229);
    term.key("keyup", "c", 67);
    term.key("keyup", "d", 68);
  }
  term.key("keydown", "Enter", 13);
  await delay(5);
  assert.equal(term.sent.join(""), `${expected}\r`);
});

test("IME insertText is not repeated by a pending textarea fallback", async (context) => {
  const term = openTerminal(context);
  term.key("keydown", "c", 229);
  term.insert("c");
  term.key("keyup", "c", 67);
  term.insert("d");
  term.key("keydown", "d", 229);
  await delay(5);
  assert.equal(term.sent.join(""), "cd");
});

test("active and pending composition emit only the final commit", async (context) => {
  const term = openTerminal(context);
  term.composition("compositionstart", "");
  term.composition("compositionupdate", "ni");
  term.insert("你", "insertCompositionText", true);
  term.composition("compositionend", "你");
  term.insert("!", "insertText");
  await delay(5);
  assert.equal(term.sent.join(""), "你!");
});

test("plain printable keydown and Enter retain their terminal sequences", (context) => {
  const term = openTerminal(context);
  assert.equal(term.key("keydown", "c", 67), true);
  assert.equal(term.key("keydown", "Enter", 13), true);
  assert.deepEqual(term.sent, ["c", "\r"]);
});
