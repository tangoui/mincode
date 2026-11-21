import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import ts from "typescript";

const sourcePath = new URL("../src/domain/ai/session-config-option.ts", import.meta.url);
const source = await readFile(sourcePath, "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText;
const moduleUrl = `data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}`;
const { acpConfigCurrentLabel, groupAcpConfigSelectOptions, parseAcpConfigSelectOptions } = await import(moduleUrl);

test("reads the select options an ACP agent reports", () => {
  const options = parseAcpConfigSelectOptions([
    {
      id: "model",
      name: "Model",
      category: "model",
      type: "select",
      currentValue: "opencode/big-pickle",
      options: [
        { value: "opencode/big-pickle", name: "OpenCode Zen/Big Pickle" },
        { value: "opencode/hy3-free", name: "OpenCode Zen/Hy3 Free" },
      ],
    },
  ]);

  assert.equal(options.length, 1);
  assert.equal(options[0].id, "model");
  assert.equal(options[0].category, "model");
  assert.equal(options[0].currentValue, "opencode/big-pickle");
  assert.deepEqual(
    options[0].choices.map((choice) => choice.value),
    ["opencode/big-pickle", "opencode/hy3-free"],
  );
});

test("keeps choice descriptions when the agent supplies them", () => {
  const [option] = parseAcpConfigSelectOptions([
    {
      id: "mode",
      name: "Session Mode",
      type: "select",
      currentValue: "build",
      options: [
        { value: "build", name: "build", description: "The default agent." },
        { value: "plan", name: "plan" },
      ],
    },
  ]);

  assert.equal(option.choices[0].description, "The default agent.");
  assert.equal(option.choices[1].description, undefined);
});

test("falls back to the value when an agent omits a choice label", () => {
  const [option] = parseAcpConfigSelectOptions([
    { id: "model", type: "select", currentValue: "a", options: [{ value: "a" }] },
  ]);

  assert.equal(option.name, "model");
  assert.equal(option.choices[0].label, "a");
});

test("drops options the drawer cannot render", () => {
  assert.deepEqual(parseAcpConfigSelectOptions(undefined), []);
  assert.deepEqual(parseAcpConfigSelectOptions("nope"), []);
  assert.deepEqual(
    parseAcpConfigSelectOptions([
      { id: "unstable", type: "boolean", currentValue: true },
      { id: "", type: "select", options: [{ value: "a" }] },
      { id: "empty", type: "select", options: [] },
      { id: "blank-choice", type: "select", options: [{ value: "  " }] },
    ]),
    [],
  );
});

test("tolerates a missing currentValue so the control still renders", () => {
  const [option] = parseAcpConfigSelectOptions([
    { id: "model", type: "select", options: [{ value: "a", name: "A" }] },
  ]);

  assert.equal(option.currentValue, "");
  assert.equal(option.choices.length, 1);
});

const option = (id, category, currentValue = "a") => ({
  id,
  name: id,
  ...(category ? { category } : {}),
  currentValue,
  choices: [{ value: "a", label: "A" }, { value: "b", label: "B" }],
});

test("merges the model and thought-level pickers into one section list", () => {
  const mode = option("mode", "mode");
  const model = option("model", "model");
  const thought = option("thought", "thought_level", "b");

  const { merged, standalone } = groupAcpConfigSelectOptions([mode, thought, model]);

  assert.deepEqual(merged.map((item) => item.id), ["thought", "model"]);
  assert.deepEqual(standalone.map((item) => item.id), ["mode"]);
});

test("leaves a lone model picker on its own so it stays one click away", () => {
  const options = [option("mode", "mode"), option("model", "model")];

  const { merged, standalone } = groupAcpConfigSelectOptions(options);

  assert.deepEqual(merged, []);
  assert.deepEqual(standalone, options);
});

test("reads the label of the currently selected choice", () => {
  assert.equal(acpConfigCurrentLabel(option("model", "model", "b")), "B");
  assert.equal(acpConfigCurrentLabel(option("model", "model", "missing")), "");
});
