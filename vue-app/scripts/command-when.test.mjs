import assert from "node:assert/strict";
import test from "node:test";
import {
  evaluateWhen,
  parseWhenExpression,
  WHEN_EXPRESSION_MAX_CLAUSES,
  WHEN_EXPRESSION_MAX_LENGTH,
} from "../src/domain/commands/when.ts";
import {
  commandInputInitialValues,
  parseCommandInputForm,
  serializeCommandInputValues,
  validateCommandInputValues,
} from "../src/domain/commands/input-schema.ts";

test("when parser accepts the supported dialect", () => {
  for (const expression of [
    "connected",
    "!connected",
    'mode == "debug"',
    'mode != "debug" && connected',
    "a && b && c",
  ]) {
    assert.ok(parseWhenExpression(expression).length > 0, expression);
  }
});

test("when parser rejects unsupported syntax", () => {
  for (const expression of [
    "",
    "&&",
    "connected &&",
    "&& connected",
    "connected || ready",
    'mode = "debug"',
    "mode == debug",
    'mode == "unterminated',
    "!",
    "Connected",
    "a".repeat(WHEN_EXPRESSION_MAX_LENGTH + 1),
    Array.from({ length: WHEN_EXPRESSION_MAX_CLAUSES + 1 }, (_, index) => `k${index}`).join(" && "),
  ]) {
    assert.throws(() => parseWhenExpression(expression), undefined, expression);
  }
});

test("when evaluation mirrors host semantics", () => {
  const context = { connected: true, mode: "debug", count: 0 };
  const cases = [
    ["connected", true],
    ["!connected", false],
    ["missing", false],
    ["!missing", true],
    ["count", false],
    ['mode == "debug"', true],
    ['mode == "release"', false],
    ['mode != "release"', true],
    ['missing != "release"', true],
    ['connected == "true"', false],
    ['connected && mode == "debug"', true],
    ['connected && mode == "release"', false],
    ["connected && missing", false],
  ];
  for (const [expression, want] of cases) {
    assert.equal(evaluateWhen(expression, context), want, expression);
  }
});

test("malformed when expressions hide the command", () => {
  assert.equal(evaluateWhen("connected ||", { connected: true }), false);
  assert.equal(evaluateWhen("", {}), true);
  assert.equal(evaluateWhen("   ", {}), true);
});

const validSchema = {
  type: "object",
  required: ["name"],
  properties: {
    name: { type: "string", description: "名称" },
    retry: { type: "integer", default: 3 },
    level: { type: "string", enum: ["info", "error"] },
    force: { type: "boolean" },
  },
};

test("input schema parses into a form model", () => {
  const form = parseCommandInputForm(validSchema);
  assert.ok(form);
  assert.equal(form.properties.length, 4);
  assert.equal(form.properties.find((p) => p.name === "name")?.required, true);
  assert.equal(form.properties.find((p) => p.name === "retry")?.default, 3);
});

test("input schema rejects non-renderable shapes", () => {
  for (const schema of [
    { type: "string" },
    { type: "object" },
    { type: "object", properties: {} },
    { type: "object", properties: { nested: { type: "object" } } },
    { type: "object", properties: { bad: { type: "string", enum: [] } } },
    { type: "object", properties: { bad: { type: "string", default: 1 } } },
    { type: "object", properties: { bad: { type: "string", enum: [1] } } },
  ]) {
    assert.equal(parseCommandInputForm(schema), null, JSON.stringify(schema));
  }
});

test("input form values validate and serialize", () => {
  const form = parseCommandInputForm(validSchema);
  assert.ok(form);
  const initial = commandInputInitialValues(form);
  assert.equal(initial.retry, 3);
  assert.equal(initial.force, false);
  assert.equal(initial.name, undefined);

  assert.deepEqual(validateCommandInputValues(form, initial), { property: "name", reason: "required" });

  const filled = { ...initial, name: "demo", level: "info" };
  assert.equal(validateCommandInputValues(form, filled), null);
  assert.equal(serializeCommandInputValues(form, filled), '{"name":"demo","retry":3,"level":"info","force":false}');

  const badType = { ...filled, retry: 1.5 };
  assert.deepEqual(validateCommandInputValues(form, badType), { property: "retry", reason: "type" });

  // Optional empty strings are omitted from the payload.
  const sparse = { ...filled, level: "" };
  assert.equal(serializeCommandInputValues(form, { ...sparse, level: undefined }), '{"name":"demo","retry":3,"force":false}');
});
