import assert from "node:assert/strict";
import test from "node:test";
import { isRetiredAiModel, withoutRetiredAiModels } from "../src/domain/ai/model-selection.ts";

const preset = { model: "gpt-6-astra", commonModels: ["gpt-6-astra", "gpt-6.1-sol", "gpt-6-luna"] };

test("OpenAI models before GPT-5 are retired, also behind a router prefix", () => {
  for (const model of ["gpt-3.5-turbo", "gpt-4", "gpt-4-turbo", "gpt-4o", "gpt-4o-mini", "gpt-4.1", "gpt-4.1-mini",
    "chatgpt-4o-latest", "o1", "o1-mini", "o3", "o3-mini", "o4-mini", "openai/gpt-4.1-mini", " GPT-4o "]) {
    assert.equal(isRetiredAiModel(model), true, model);
  }
  for (const model of ["gpt-5", "gpt-5.1", "gpt-6-astra", "openai/gpt-6.1-sol", "gpt-oss:20b", "openai/gpt-oss-120b",
    "omni", "claude-opus-5-5", "qwen3.8-max", "o5-preview", ""]) {
    assert.equal(isRetiredAiModel(model), false, model);
  }
});

test("a profile keeps its newer models and drops retired ones everywhere", () => {
  const next = withoutRetiredAiModels({
    model: "gpt-4.1", commonModels: ["gpt-4.1", "gpt-5.1", "o4-mini", "my-proxy-model"],
    failoverModels: ["o4-mini", "my-proxy-model"], subagentModel: "gpt-4.1-mini",
  }, preset);
  assert.deepEqual(next, {
    model: "gpt-5.1", commonModels: ["gpt-5.1", "my-proxy-model"],
    failoverModels: ["my-proxy-model"], subagentModel: "",
  });
});

test("a profile with only retired models gets the provider's current models", () => {
  const next = withoutRetiredAiModels({
    model: "gpt-4.1-mini", commonModels: ["gpt-4.1-mini", "gpt-4.1", "o4-mini"], failoverModels: ["gpt-4.1"], subagentModel: "",
  }, preset);
  assert.deepEqual(next, { model: "gpt-6-astra", commonModels: preset.commonModels, failoverModels: [], subagentModel: "" });
});

test("a profile without retired models is returned unchanged", () => {
  const profile = { model: "gpt-6-astra", commonModels: ["gpt-6-astra"], failoverModels: [], subagentModel: "" };
  assert.equal(withoutRetiredAiModels(profile, preset), profile);
});
