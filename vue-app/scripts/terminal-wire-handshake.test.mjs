import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { parseWireHandshakeError } from "../src/domain/terminal/wire-handshake.ts";

const [localTerminalSource, sshTerminalSource, dockerTerminalSource, wireSource] = await Promise.all([
  readFile(new URL("../src/components/terminal/LocalTerminal.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/components/terminal/SshTerminal.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/components/ssh/SshDockerTerminal.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/services/terminal-wire.ts", import.meta.url), "utf8"),
]);

/** Body of the first `xterm.onData((...) => { ... })` callback, by brace matching. */
function onDataBody(source) {
  const start = source.indexOf("xterm.onData(");
  assert.notEqual(start, -1, "xterm.onData not found");
  const open = source.indexOf("{", start);
  let depth = 0;
  for (let index = open; index < source.length; index += 1) {
    if (source[index] === "{") depth += 1;
    else if (source[index] === "}" && --depth === 0) return source.slice(open, index + 1);
  }
  throw new Error("unbalanced onData callback");
}

test("extracts backend handshake rejection reasons", () => {
  const encoder = new TextEncoder();

  assert.equal(
    parseWireHandshakeError(
      encoder.encode('{"error":"session not found","sessionId":"terminal-x"}')
    ),
    "session not found"
  );
  assert.equal(parseWireHandshakeError('{"error":"session not found"}'), "session not found");
});

test("local and SSH terminals use reliable IPC input and event output channels", () => {
  for (const source of [localTerminalSource, sshTerminalSource]) {
    assert.match(source, /createTerminalInputBatcher\(writeShellInput/);
    assert.match(source, /invokeBackend<void>\("write_to_shell"/);
    assert.match(source, /listenAppEvent<ShellOutputEvent>\(shellOutputEventName\(props\.sessionId\)/);
    assert.doesNotMatch(source, /openTerminalWire/);
    assert.doesNotMatch(source, /wire\?\.(?:sendInput|sendResize)/);
    assert.match(source, /void queueShellInput\(input, isImmediateTerminalInput\(input\)\)\.catch/);
  }
  assert.match(wireSource, /socket\.readyState !== socket\.OPEN/);
});

test("SSH terminal predicts echo for typed input and parses output through the binding", () => {
  assert.match(sshTerminalSource, /usePredictiveEcho\(/);
  assert.match(sshTerminalSource, /predictiveEcho\.write\(data\)/);
  assert.doesNotMatch(sshTerminalSource, /xterm\?\.write\(data\)/);
  const body = onDataBody(sshTerminalSource);
  const guard = body.indexOf('if (closed || status.value !== "ready") return;');
  const predicted = body.indexOf("if (!isTerminalReport(input)) predictiveEcho.input(input);");
  const queued = body.indexOf("void queueShellInput(input, isImmediateTerminalInput(input))");
  assert.ok(guard >= 0 && predicted > guard, "prediction runs after the ready guard");
  // Terminal auto-replies (CPR, DA, focus, mouse reports) are not keystrokes: they must not clear predictions.
  assert.equal(body.match(/predictiveEcho\.input\(/g)?.length, 1);
  assert.ok(queued > predicted, "prediction runs before the input is queued");
  // Programmatic pastes never reach xterm directly, so they are never predicted.
  assert.doesNotMatch(sshTerminalSource, /xterm\??\.paste\(/);
  assert.match(sshTerminalSource, /predictiveEcho\.reset\(\)/);
  assert.match(sshTerminalSource, /registerTerminalBufferReader\(props\.sessionId/);
  // Input-failure status lines move the cursor, so they clear predictions first.
  assert.equal(sshTerminalSource.match(/\[MinCode\] \$\{t\("terminal\.inputFailed"\)\}/g)?.length, 1);
  assert.match(sshTerminalSource, /function writeInputFailure\(\) \{\n  predictiveEcho\.reset\(\);\n  xterm\?\.writeln\(/);
});

test("docker terminal predicts echo only in exec mode", () => {
  assert.match(dockerTerminalSource, /usePredictiveEcho\(/);
  assert.match(dockerTerminalSource, /enabled: \(\) => props\.mode === "exec"/);
  assert.match(dockerTerminalSource, /if \(props\.mode === "exec"\) predictiveEcho\.write\(payload\.data\);\s*else xterm\?\.write\(payload\.data\);/);
  const exec = dockerTerminalSource.indexOf('if (props.mode === "exec") {\n    inputSubscription = xterm.onData(');
  assert.notEqual(exec, -1, "onData is subscribed only in the exec branch");
  const body = onDataBody(dockerTerminalSource);
  const predicted = body.indexOf("if (!isTerminalReport(data)) predictiveEcho.input(data);");
  assert.ok(predicted > body.indexOf("if (!started || closed) return;"), "terminal reports are not predicted");
  assert.ok(body.indexOf("queueExecInput(data)") > predicted);
  assert.match(dockerTerminalSource, /import \{ isTerminalReport \} from "@\/domain\/terminal\/terminal-attention";/);
  assert.doesNotMatch(dockerTerminalSource, /xterm\??\.paste\(/);
  assert.match(dockerTerminalSource, /function writeError\(message: string\) \{\n(?:\s*\/\/.*\n)?  resetPrediction\(\);\n  xterm\?\.writeln\(/);
});

test("local terminal does not use predictive echo", () => {
  assert.doesNotMatch(localTerminalSource, /usePredictiveEcho/);
});

test("ignores wire frames and malformed payloads", () => {
  // Wire v1 二进制帧（version=1,type=1,len=2）不应被当作握手错误。
  const wireFrame = new Uint8Array([1, 1, 0, 0, 0, 2, 0x41, 0x42]);
  assert.equal(parseWireHandshakeError(wireFrame.buffer), null);

  assert.equal(parseWireHandshakeError("not json"), null);
  assert.equal(parseWireHandshakeError('{"error":""}'), null);
  assert.equal(parseWireHandshakeError('{"other":1}'), null);
  assert.equal(parseWireHandshakeError(""), null);
});
