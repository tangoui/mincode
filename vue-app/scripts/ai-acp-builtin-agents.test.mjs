import assert from "node:assert/strict";
import test from "node:test";
import {
  ACP_BUILTIN_AGENTS,
  acpBuiltinAgentCommandLine,
  acpBuiltinAgentLaunch,
} from "../src/domain/ai/acp-agents.ts";

test("keeps the existing built-in launch commands unchanged", () => {
  // @zed-industries/claude-code-acp was renamed and no longer picks up the
  // user's Claude login ("Authentication required").
  assert.deepEqual(acpBuiltinAgentLaunch("claude-code"), {
    command: "npx",
    args: ["--yes", "@agentclientprotocol/claude-agent-acp"],
  });
  assert.deepEqual(acpBuiltinAgentLaunch("codex"), {
    command: "npx",
    args: ["--yes", "@agentclientprotocol/codex-acp"],
  });
  assert.deepEqual(acpBuiltinAgentLaunch("opencode"), { command: "opencode", args: ["acp"] });
});

test("launches the common ACP agents with their registry arguments", () => {
  assert.deepEqual(acpBuiltinAgentLaunch("gemini"), { command: "gemini", args: ["--acp"] });
  assert.deepEqual(acpBuiltinAgentLaunch("qwen-code"), { command: "qwen", args: ["--acp"] });
  assert.deepEqual(acpBuiltinAgentLaunch("goose"), { command: "goose", args: ["acp"] });
  assert.deepEqual(acpBuiltinAgentLaunch("auggie"), { command: "auggie", args: ["--acp"] });
  assert.deepEqual(acpBuiltinAgentLaunch("kimi"), { command: "kimi", args: ["acp"] });
  assert.deepEqual(acpBuiltinAgentLaunch("pi"), { command: "npx", args: ["--yes", "pi-acp"] });
});

test("falls back to Claude Code for unknown or empty adapter IDs", () => {
  const fallback = acpBuiltinAgentLaunch("claude-code");
  assert.deepEqual(acpBuiltinAgentLaunch(""), fallback);
  assert.deepEqual(acpBuiltinAgentLaunch("custom:missing"), fallback);
  assert.deepEqual(acpBuiltinAgentLaunch("toString"), fallback);
});

test("uses unique IDs and returns copies of launch arguments", () => {
  assert.equal(new Set(ACP_BUILTIN_AGENTS.map((agent) => agent.id)).size, ACP_BUILTIN_AGENTS.length);
  assert.equal(ACP_BUILTIN_AGENTS[0].id, "claude-code");
  const launch = acpBuiltinAgentLaunch("gemini");
  launch.args.push("--mutated");
  assert.deepEqual(acpBuiltinAgentLaunch("gemini").args, ["--acp"]);
});

test("shows the command line for each built-in card", () => {
  const gemini = ACP_BUILTIN_AGENTS.find((agent) => agent.id === "gemini");
  assert.equal(acpBuiltinAgentCommandLine(gemini), "gemini --acp");
});
