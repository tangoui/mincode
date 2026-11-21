import assert from "node:assert/strict";
import test from "node:test";
import {
  echoProbe,
  parseSmokeConfig,
  plainTerminalText,
  summarizeSmoke,
} from "../src/domain/e2e/desktop-smoke.ts";

const base = { scenarios: ["terminal"], nonce: "abc123", timeoutMs: 5000 };

test('formatter smoke requires explicit disposable package and image', () => {
 assert.throws(() => parseSmokeConfig({...base, scenarios:['formatter']}), /formatter/);
 const formatter = {packagePath:'/tmp/p', imagePath:'/tmp/image.png'};
 assert.deepEqual(parseSmokeConfig({...base, scenarios:['formatter'], formatter}).formatter, formatter);
});

test("smoke config requires known scenarios and a fixture for each", () => {
  assert.deepEqual(parseSmokeConfig(base), { scenarios: ["terminal"], nonce: "abc123", timeoutMs: 5000 });
  assert.equal(parseSmokeConfig({ ...base, timeoutMs: -1 }).timeoutMs, 30000);
  for (const invalid of [null, [], {}, { ...base, scenarios: [] }, { ...base, scenarios: ["unknown"] }, { ...base, nonce: "bad nonce" }]) {
    assert.throws(() => parseSmokeConfig(invalid), /invalid smoke config/);
  }
  assert.throws(() => parseSmokeConfig({ ...base, scenarios: ["ssh"] }), /ssh\.host/);
  assert.throws(() => parseSmokeConfig({ ...base, scenarios: ["database"] }), /database\.filePath/);
  assert.throws(() => parseSmokeConfig({ ...base, scenarios: ["plugin"], plugin: { packagePath: "/p" } }), /plugin\.pluginId/);
  const ssh = parseSmokeConfig({
    ...base, scenarios: ["ssh", "ssh"], ssh: { host: "127.0.0.1", port: 2222, username: "u", password: "p" },
  });
  assert.deepEqual(ssh.scenarios, ["ssh"]);
  assert.throws(() => parseSmokeConfig({ ...base, scenarios: ["ssh"], ssh: { host: "h", port: 70000, username: "u", password: "p" } }), /ssh\.port/);
});

test("the AI fixture must be a loopback http endpoint", () => {
  const ai = { model: "m", apiKey: "k", expectedText: "t", slowPrompt: "s" };
  assert.equal(parseSmokeConfig({ ...base, scenarios: ["ai"], ai: { ...ai, baseUrl: "http://127.0.0.1:9/v1" } }).ai.baseUrl, "http://127.0.0.1:9/v1");
  assert.throws(() => parseSmokeConfig({ ...base, scenarios: ["ai"], ai: { ...ai, baseUrl: "https://api.example.com/v1" } }), /loopback/);
});

test("browser smoke requires explicit loopback fixture URLs", () => {
  assert.throws(() => parseSmokeConfig({ ...base, scenarios: ["browser"] }), /browser.firstUrl/);
  const browser = { firstUrl: "http://127.0.0.1:9001/first", secondUrl: "http://127.0.0.1:9001/second", errorUrl: "http://127.0.0.1:9001/navigation-error" };
  assert.deepEqual(parseSmokeConfig({ ...base, scenarios: ["browser"], browser }).browser, browser);
  for (const field of ["firstUrl", "secondUrl", "errorUrl"]) {
    assert.throws(() => parseSmokeConfig({ ...base, scenarios: ["browser"], browser: { ...browser, [field]: "https://example.com/" } }), /loopback/);
  }
});

test("echo probes cannot be satisfied by the echoed command line", () => {
  const probe = echoProbe("n0nce", "LOCAL");
  assert.equal(probe.expected, "MINCODE_LOCAL_42_n0nce");
  assert.equal(probe.command.includes(probe.expected), false);
  assert.equal(plainTerminalText(`\x1b[32m${probe.command}\x1b[0m\r\n${probe.expected}\r\n\x1b]0;title\x07`), `${probe.command}\n${probe.expected}\n`);
});

test("smoke summaries count each status", () => {
  assert.deepEqual(summarizeSmoke([
    { scenario: "ai", status: "passed", durationMs: 1, steps: [] },
    { scenario: "ssh", status: "failed", durationMs: 1, steps: [], error: "x" },
    { scenario: "terminal", status: "skipped", durationMs: 0, steps: [] },
  ]), { passed: 1, failed: 1, skipped: 1 });
});

test("association smoke requires explicit isolated fixtures and phase", () => {
  assert.throws(() => parseSmokeConfig({ ...base, scenarios: ["associations"] }), /associations.phase/);
  const fixture = { phase: "setup", packagePath: "/tmp/p.min", torrentPath: "/tmp/a.torrent", ordinaryPath: "/tmp/a.txt" };
  assert.deepEqual(parseSmokeConfig({ ...base, scenarios: ["associations"], associations: fixture }).associations, fixture);
});

test("ebook smoke requires an explicit package and its fixture paths", () => {
 assert.throws(()=>parseSmokeConfig({...base,scenarios:["ebook"]}),/ebook/);
 const ebook={packagePath:"/tmp/p",paths:["/tmp/a.chm","/tmp/a.epub","/tmp/a.txt"]};
 assert.deepEqual(parseSmokeConfig({...base,scenarios:["ebook"],ebook}).ebook,ebook);
 assert.throws(()=>parseSmokeConfig({...base,scenarios:["ebook"],ebook:{...ebook,paths:[]}}),/ebook\.paths/);
 assert.throws(()=>parseSmokeConfig({...base,scenarios:["ebook"],ebook:{...ebook,paths:Array(17).fill("/tmp/a.txt")}}),/ebook\.paths/);
});

test("AI catalog smoke requires an explicit fixture", () => {
  assert.throws(() => parseSmokeConfig({ ...base, scenarios: ["ai-catalogs"] }), /aiCatalogs.packagePath/);
  assert.deepEqual(parseSmokeConfig({ ...base, scenarios: ["ai-catalogs"], aiCatalogs: { packagePath: "/tmp/catalog" } }).aiCatalogs, { packagePath: "/tmp/catalog" });
});


test('custom AI prompts smoke requires an isolated plugin package', () => {
 assert.throws(() => parseSmokeConfig({ ...base, scenarios: ['ai-prompts'] }), /aiPrompts.packagePath/);
 assert.deepEqual(parseSmokeConfig({ ...base, scenarios: ['ai-prompts'], aiPrompts: { packagePath: '/tmp/prompts' } }).aiPrompts, { packagePath: '/tmp/prompts' });
});

test("Rust plugin smoke requires a real compiled package", () => {
 assert.throws(()=>parseSmokeConfig({...base,scenarios:["rust-plugin"]}),/rustPlugin.packagePath/);
 const fixture={packagePath:"/tmp/rust/dist-plugin",pluginId:"rust-plugin"};
 assert.deepEqual(parseSmokeConfig({...base,scenarios:["rust-plugin"],rustPlugin:fixture}).rustPlugin,fixture);
});

test("TypeScript plugin smoke requires a built package", () => {
 assert.throws(()=>parseSmokeConfig({...base,scenarios:["ts-plugin"]}),/tsPlugin.packagePath/);
 assert.deepEqual(parseSmokeConfig({...base,scenarios:["ts-plugin"],tsPlugin:{packagePath:"/tmp/ts/dist-plugin"}}).tsPlugin,{packagePath:"/tmp/ts/dist-plugin"});
});

test("lsp smoke requires its project fixture", () => {
  assert.throws(() => parseSmokeConfig({ ...base, scenarios: ["lsp"] }), /lsp\.projectRoot/);
  assert.deepEqual(parseSmokeConfig({ ...base, scenarios: ["lsp"], lsp: { projectRoot: "/tmp/p" } }).lsp, { projectRoot: "/tmp/p" });
});

test("perf-remote accepts an optional laggy sshd port for the predictive echo block", () => {
  const ssh = { host: "127.0.0.1", port: 2222, username: "u", password: "p" };
  const perfRemote = { remoteDir: "/tmp/r", remoteFiles: 5, only: "typing" };
  const plain = parseSmokeConfig({ ...base, scenarios: ["perf-remote"], ssh, perfRemote });
  assert.equal(plain.perfRemote.laggySsh, undefined);
  const laggy = parseSmokeConfig({ ...base, scenarios: ["perf-remote"], ssh, perfRemote: { ...perfRemote, laggySsh: { port: 2223 } } });
  assert.deepEqual(laggy.perfRemote.laggySsh, { port: 2223 });
  assert.equal(laggy.perfRemote.only, "typing");
  assert.throws(() => parseSmokeConfig({ ...base, scenarios: ["perf-remote"], ssh, perfRemote: { ...perfRemote, laggySsh: { port: 0 } } }), /perfRemote\.laggySsh\.port/);
});
