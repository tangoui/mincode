import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

const panelSource = await readFile(
  new URL("../src/components/projects/ProjectGitPanel.vue", import.meta.url),
  "utf8",
);

test("loads compact Git context and AI settings in parallel", () => {
  const start = panelSource.indexOf("async function generateCommitMessage() {");
  const end = panelSource.indexOf("async function handleActionCommand", start);
  const generationSource = panelSource.slice(start, end);

  assert.ok(start >= 0 && end > start, "commit message generation source must exist");
  // Parallel, and abortable: cancel and the overall timeout also end this stage.
  assert.ok(generationSource.includes("const [patch] = await runWithAbort(controller.signal, () => Promise.all(["));
  assert.ok(generationSource.includes('"git_get_commit_message_context"'));
  assert.ok(generationSource.includes("hydrateSettingsSnapshot(),"));
  assert.ok(!generationSource.includes('"git_get_staged_patch"'));
});

test("commit messages use the model picked in the AI assistant with its failover chain", () => {
  const start = panelSource.indexOf("function commitMessageModelPlan()");
  const plan = panelSource.slice(start, panelSource.indexOf("function currentAiConfig", start));
  assert.ok(start >= 0);
  assert.match(plan, /selectedModel: aiAssistant\.selectedModel/);
  assert.match(plan, /buildAiModelAttemptOrder\(/);
  assert.match(panelSource, /config: plan\.config,\s*models: plan\.models,/);
  assert.match(panelSource, /t\("git\.aiGenerateTimeout", \{ model: generatingModel \|\| "AI" \}\)/);
});

test("the repository draft is always the reactive proxy", () => {
  // `(record[key] = value)` returns the raw object: a first-time draft was mutated
  // outside Vue's tracking and the button kept spinning after the run ended.
  const start = panelSource.indexOf("function repositoryCommitDraft(path: string) {");
  const body = panelSource.slice(start, panelSource.indexOf("\n}", start));
  assert.ok(start >= 0);
  assert.doesNotMatch(body, /\?\?\s*\(\s*repositoryCommitDrafts\[path\]\s*=/);
  assert.match(body, /return repositoryCommitDrafts\[path\];/);
});

test("thinking models are judged by stream activity, not by the first visible token", () => {
  const start = panelSource.indexOf("async function generateCommitMessage() {");
  const generationSource = panelSource.slice(start, panelSource.indexOf("async function runQuickAction", start));
  assert.doesNotMatch(panelSource, /FIRST_TOKEN_TIMEOUT/);
  assert.match(generationSource, /idleTimeoutMs: COMMIT_MESSAGE_IDLE_TIMEOUT_MS,/);
  assert.match(generationSource, /onReasoning: \(\) => \{/);
  assert.match(generationSource, /isAiStreamIdleTimeoutError\(error\)/);
  assert.match(panelSource, /:placeholder="commitMessagePlaceholder"/);
  assert.match(panelSource, /t\("git\.aiThinking", \{ seconds:/);
});
