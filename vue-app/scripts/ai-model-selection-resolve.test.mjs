import assert from "node:assert/strict";
import test from "node:test";
import { buildAiModelAttemptOrder, resolveAiModelSelection } from "../src/domain/ai/model-selection.ts";

const profiles = [
  { id: "openai", model: "gpt-5.6-sol", commonModels: ["gpt-4.1-mini"] },
  { id: "claude", model: "claude-opus-5-5", commonModels: ["claude-sonnet-5"] },
];

test("actions without a picker follow the assistant's selected profile and model", () => {
  const resolved = resolveAiModelSelection({ profiles, activeProfileId: "openai", selectedModel: "claude::claude-sonnet-5" });
  assert.equal(resolved.profile.id, "claude");
  assert.deepEqual(resolved.selection, { profileId: "claude", model: "claude-sonnet-5" });
});

test("a missing or stale assistant selection falls back to the active profile default", () => {
  for (const selectedModel of ["", "claude::removed-model", "gone::x", undefined]) {
    const resolved = resolveAiModelSelection({ profiles, activeProfileId: "openai", selectedModel });
    assert.deepEqual(resolved.selection, { profileId: "openai", model: "gpt-5.6-sol" });
  }
});

test("the attempt order starts with the selection and appends configured failovers", () => {
  assert.deepEqual(buildAiModelAttemptOrder({
    selectedModel: "claude-sonnet-5",
    profileDefaultModel: "claude-opus-5-5",
    configuredModels: ["claude-opus-5-5", "claude-sonnet-5"],
    fallbackEnabled: true,
    fallbackModels: ["claude-opus-5-5"],
  }), ["claude-sonnet-5", "claude-opus-5-5"]);
});
