import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { alternateScreenWheelInput } from "../src/domain/terminal/alternate-screen-wheel.ts";

const [localTerminal, sshTerminal] = await Promise.all([
  readFile(new URL("../src/components/terminal/LocalTerminal.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/components/terminal/SshTerminal.vue", import.meta.url), "utf8"),
]);

test("alternate screen without mouse tracking maps wheel to page navigation", () => {
  assert.equal(alternateScreenWheelInput("alternate", "none", -120), "\u001b[5~");
  assert.equal(alternateScreenWheelInput("alternate", "none", 120), "\u001b[6~");
});

test("normal buffers and mouse-tracking TUIs keep xterm's native wheel path", () => {
  assert.equal(alternateScreenWheelInput("normal", "none", 120), null);
  assert.equal(alternateScreenWheelInput("alternate", "any", 120), null);
  assert.equal(alternateScreenWheelInput("alternate", "drag", -120), null);
  assert.equal(alternateScreenWheelInput("alternate", "none", 120, true), null);
});

test("local and SSH terminals install the alternate-screen wheel bridge", () => {
  for (const source of [localTerminal, sshTerminal]) {
    assert.match(source, /attachCustomWheelEventHandler\(\(event\) =>/);
    assert.match(source, /alternateScreenWheelInput\(/);
  }
});
