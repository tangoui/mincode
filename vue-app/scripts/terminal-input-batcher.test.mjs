import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import {
  createTerminalInputBatcher,
  isImmediateTerminalInput,
} from "../src/domain/terminal/input-batcher.ts";

const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

test("sends an idle keystroke at once and batches input typed while a write is in flight", async () => {
  const writes = [];
  let release;
  const slow = new Promise((resolve) => { release = resolve; });
  const batcher = createTerminalInputBatcher(async (input) => {
    writes.push(input);
    if (input === "a") await slow;
  }, { delayMs: 20, maxChars: 256 });

  batcher.push("a");
  assert.deepEqual(writes, [], "the write starts on the next microtask, not after a timer");
  await Promise.resolve();
  await Promise.resolve();
  assert.deepEqual(writes, ["a"]);
  batcher.push("b");
  const last = batcher.push("c");
  await wait(5);
  assert.deepEqual(writes, ["a"], "nothing else goes out while the first write is pending");
  release();
  await last;
  assert.deepEqual(writes, ["a", "bc"]);
  batcher.dispose();
});

test("a control key typed during a pending write goes out right behind it with the batch", async () => {
  const writes = [];
  let release;
  const slow = new Promise((resolve) => { release = resolve; });
  const batcher = createTerminalInputBatcher(async (input) => {
    writes.push(input);
    if (input === "l") await slow;
  });

  batcher.push("l");
  batcher.push("s");
  const enter = batcher.push("\r", true);
  await wait(5);
  assert.deepEqual(writes, ["l"]);
  release();
  await enter;
  await wait(1);
  assert.deepEqual(writes, ["l", "s\r"]);
  batcher.dispose();
});

test("idle keystrokes do not wait for any timer", async () => {
  const writes = [];
  const batcher = createTerminalInputBatcher(async (input) => { writes.push(input); }, { delayMs: 1000 });
  await batcher.push("x");
  await batcher.push("y");
  assert.deepEqual(writes, ["x", "y"]);
  batcher.dispose();
});

test("serializes a later batch behind a slow confirmed write", async () => {
  const writes = [];
  let release;
  const first = new Promise((resolve) => { release = resolve; });
  const batcher = createTerminalInputBatcher(async (input) => {
    writes.push(input);
    if (input === "abc") await first;
  }, { delayMs: 1 });

  batcher.push("abc", true);
  const second = batcher.push("def", true);
  await wait(5);
  assert.deepEqual(writes, ["abc"]);
  release();
  await second;
  assert.deepEqual(writes, ["abc", "def"]);
  batcher.dispose();
});

test("reports confirmed write errors without dropping later input", async () => {
  const errors = [];
  const writes = [];
  const batcher = createTerminalInputBatcher(async (input) => {
    writes.push(input);
    if (input === "bad") throw new Error("write failed");
  }, { delayMs: 1, onError: (error) => errors.push(error.message) });

  await assert.rejects(batcher.push("bad", true));
  await batcher.push("good", true);
  assert.deepEqual(writes, ["bad", "good"]);
  assert.deepEqual(errors, []);
  batcher.dispose();
});

test("control keys flush immediately while printable input is batched", () => {
  for (const input of ["\x03", "\x04", "\b", "\n", "\r", "\x1b[A", "ls\r"]) {
    assert.equal(isImmediateTerminalInput(input), true, JSON.stringify(input));
  }
  for (const input of ["a", "hello", "中文"]) assert.equal(isImmediateTerminalInput(input), false);
});

test("the docker exec terminal batches keystrokes like the other terminals", async () => {
  const source = await readFile(
    new URL("../src/components/ssh/SshDockerTerminal.vue", import.meta.url),
    "utf8",
  );
  assert.match(source, /createTerminalInputBatcher\(writeExecInput/);
  assert.match(source, /inputBatcher\.push\(input, isImmediateTerminalInput\(input\)\)/);
  assert.match(source, /xterm\.onData\(\(data\) => \{[\s\S]{0,300}?queueExecInput\(data\)/);
  assert.match(source, /inputBatcher\?\.dispose\(\);/);
});
