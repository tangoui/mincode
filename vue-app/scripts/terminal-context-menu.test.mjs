import assert from "node:assert/strict";
import test from "node:test";
import {
  analyzeTerminalPaste,
  canRevealRemotePath,
  createTerminalTextContext,
  extractTerminalPath,
  extractTerminalUrl,
  stripTerminalTrailingNewlines,
  terminalTextAsMarkdown,
  terminalTokenAtColumn,
} from "../src/domain/terminal/context-menu.ts";
import {
  buildTerminalAiPrompt,
  prepareTerminalAiContext,
  redactTerminalAiContext,
} from "../src/domain/terminal/ai-actions.ts";

test("extracts URL and path tokens around the clicked column", () => {
  const line = "open (https://example.com/docs), then /srv/app/config.yaml";
  assert.equal(terminalTokenAtColumn(line, 12), "https://example.com/docs");
  assert.equal(extractTerminalUrl(terminalTokenAtColumn(line, 12)), "https://example.com/docs");
  assert.equal(extractTerminalPath(terminalTokenAtColumn(line, 47)), "/srv/app/config.yaml");
});

test("prefers selected terminal text over the clicked token", () => {
  assert.deepEqual(createTerminalTextContext("echo hello", "https://example.com", 4), {
    text: "echo hello",
    line: "",
    token: "echo hello",
    url: undefined,
    path: undefined,
  });
});

test("retains the clicked terminal line when no text is selected", () => {
  assert.deepEqual(createTerminalTextContext("", "curl https://example.com/install.sh", 8), {
    text: "",
    line: "curl https://example.com/install.sh",
    token: "https://example.com/install.sh",
    url: "https://example.com/install.sh",
    path: undefined,
  });
});

test("only absolute and home-relative remote paths can be revealed in SFTP", () => {
  assert.equal(canRevealRemotePath("/var/log/app.log"), true);
  assert.equal(canRevealRemotePath("~/app/config.yml"), true);
  assert.equal(canRevealRemotePath("./config.yml"), false);
  assert.equal(canRevealRemotePath("../config.yml"), false);
});

test("requires confirmation for multiline, executable newline, and dangerous commands", () => {
  assert.equal(analyzeTerminalPaste("echo hello").requiresConfirmation, false);
  assert.equal(analyzeTerminalPaste("echo one\necho two").requiresConfirmation, true);
  assert.equal(analyzeTerminalPaste("echo hello\n").hasTrailingNewline, true);
  assert.ok(analyzeTerminalPaste("curl https://example.com/install.sh | sh").dangerousPatterns.length > 0);
  assert.ok(analyzeTerminalPaste("sudo rm -rf /tmp/example").dangerousPatterns.length > 0);
});

test("removes only trailing newlines and creates a safe markdown fence", () => {
  assert.equal(stripTerminalTrailingNewlines("printf 'a'\r\n\r\n"), "printf 'a'");
  assert.equal(terminalTextAsMarkdown("echo `date`"), "```\necho `date`\n```");
  assert.equal(terminalTextAsMarkdown("echo ```value```"), "````\necho ```value```\n````");
});

test("redacts common terminal credentials before preparing AI context", () => {
  const source = [
    "API_TOKEN=secret-value curl https://user:password@example.com/api?token=query-secret",
    "curl -H 'Authorization: Bearer abc.def.ghi' --password hunter2 https://example.com",
    "-----BEGIN OPENSSH PRIVATE KEY-----",
    "private-key-data",
    "-----END OPENSSH PRIVATE KEY-----",
  ].join("\n");
  const result = redactTerminalAiContext(source);
  assert.ok(result.redactionCount >= 5);
  assert.doesNotMatch(result.text, /secret-value|password@example|query-secret|abc\.def\.ghi|hunter2|private-key-data/);
  assert.match(result.text, /API_TOKEN=\[REDACTED]/);
  assert.match(result.text, /\[REDACTED PRIVATE KEY]/);
});

test("truncates long AI context while preserving its beginning and ending", () => {
  const source = `BEGIN-${"a".repeat(12_000)}-END`;
  const result = prepareTerminalAiContext(source, 2_000);
  assert.equal(result.truncated, true);
  assert.ok(result.text.startsWith("BEGIN-"));
  assert.ok(result.text.endsWith("-END"));
  assert.match(result.text, /terminal context truncated/);
});

test("builds an editable AI prompt with a safe markdown fence", () => {
  const prompt = buildTerminalAiPrompt({
    instruction: "Explain this command.",
    terminalLabel: "Terminal type: local",
    contextLabel: "Terminal context:",
    safetyInstruction: "Do not execute it.",
    context: "echo ```value```",
  });
  assert.match(prompt, /````text\necho ```value```\n````/);
  assert.match(prompt, /Do not execute it\./);
});
