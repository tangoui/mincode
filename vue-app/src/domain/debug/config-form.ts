/**
 * The run configuration form: which control edits each property of a
 * debugger's `configurationSchema`, the text drafts the controls hold, and
 * turning valid drafts back into configuration fields.
 *
 * Kinds: `type: string` → string (with a string `enum` → enum), number,
 * integer, boolean, an array of strings → stringList (one item per line), an
 * object of string values → stringMap (one `KEY=VALUE` per line); any other
 * shape → json (JSON text). Drafts are strings except a boolean's.
 */
import type { JsonSchemaObject } from "./types.ts";

export type ConfigFormFieldKind = "string" | "number" | "integer" | "boolean" | "enum" | "stringList" | "stringMap" | "json";

export interface ConfigFormField {
  key: string;
  label: string;
  description?: string;
  required: boolean;
  kind: ConfigFormFieldKind;
  /** The values of an enum field. */
  options?: string[];
  default?: unknown;
}

/** A field's draft: the text a control holds, or a boolean switch's state. */
export type ConfigFieldDraft = string | boolean;

export type ConfigFieldDrafts = Record<string, ConfigFieldDraft>;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** The schema's single type; `["x", "null"]` reads as `x`, other unions as none. */
function singleType(schema: JsonSchemaObject | undefined): string | undefined {
  const type = schema?.type;
  if (typeof type === "string") return type;
  if (!Array.isArray(type)) return undefined;
  const types = type.filter((item) => item !== "null");
  return types.length === 1 && typeof types[0] === "string" ? types[0] : undefined;
}

function fieldKind(schema: JsonSchemaObject): Pick<ConfigFormField, "kind" | "options"> {
  const type = singleType(schema);
  if (type === "string") {
    if (!Array.isArray(schema.enum)) return { kind: "string" };
    const options = schema.enum;
    return options.length && options.every((item): item is string => typeof item === "string") ? { kind: "enum", options: [...options] } : { kind: "json" };
  }
  if (type === "number" || type === "integer" || type === "boolean") return { kind: type };
  if (type === "array" && singleType(schema.items) === "string") return { kind: "stringList" };
  const values = schema.additionalProperties;
  if (type === "object" && !schema.properties && isRecord(values) && singleType(values) === "string") return { kind: "stringMap" };
  return { kind: "json" };
}

/** The form fields of a debugger's launch or attach schema, in schema order. */
export function parseConfigForm(schema: JsonSchemaObject | undefined): ConfigFormField[] {
  if (!schema || !isRecord(schema.properties)) return [];
  const required = new Set(Array.isArray(schema.required) ? schema.required.filter((item) => typeof item === "string") : []);
  return Object.entries(schema.properties).map(([key, property]) => {
    const prop: JsonSchemaObject = isRecord(property) ? property : {};
    const title = typeof prop.title === "string" && prop.title.trim() ? prop.title.trim() : key;
    const field: ConfigFormField = { key, label: title, required: required.has(key), ...fieldKind(prop) };
    if (typeof prop.description === "string" && prop.description.trim()) field.description = prop.description.trim();
    if (prop.default !== undefined) field.default = prop.default;
    return field;
  });
}

function hasLineBreak(text: string): boolean {
  return /[\r\n]/.test(text);
}

/** A list item the one-per-line text shows and reads back unchanged. */
function isListItem(item: unknown): boolean {
  return typeof item === "string" && item !== "" && !hasLineBreak(item);
}

/** A map entry the `KEY=VALUE` line shows and reads back unchanged. */
function isMapEntry([key, item]: [string, unknown]): boolean {
  return key !== "" && key === key.trim() && !key.includes("=") && !hasLineBreak(key) && typeof item === "string" && !hasLineBreak(item);
}

/** The field's control shows value and reads it back unchanged (an absent value always fits). */
function fitsControl(field: ConfigFormField, value: unknown): boolean {
  if (value === undefined) return true;
  switch (field.kind) {
    case "boolean":
      return typeof value === "boolean";
    case "string":
    case "enum":
      return typeof value === "string";
    case "number":
    case "integer":
      return typeof value === "number" && Number.isFinite(value);
    case "stringList":
      return Array.isArray(value) && value.every(isListItem);
    case "stringMap":
      return isRecord(value) && Object.entries(value).every(isMapEntry);
    default:
      return true;
  }
}

/**
 * The form for a configuration: the schema's fields, then a json field for
 * each configuration field the schema does not describe. A value its control
 * cannot show losslessly (e.g. an empty list item, a map key with `=`, a
 * string where a number belongs) is edited as JSON instead.
 */
export function configFormFields(schema: JsonSchemaObject | undefined, values: Record<string, unknown>): ConfigFormField[] {
  const fields = parseConfigForm(schema).map((field): ConfigFormField => {
    if (fitsControl(field, values[field.key])) return field;
    const { options: _options, ...rest } = field;
    return { ...rest, kind: "json" };
  });
  const described = new Set(fields.map((field) => field.key));
  const extra = Object.keys(values)
    .filter((key) => !described.has(key))
    .map((key): ConfigFormField => ({ key, label: key, required: false, kind: "json" }));
  return [...fields, ...extra];
}

function lines(text: string): string[] {
  return text.split(/\r?\n/);
}

function stringMapText(value: unknown): string {
  if (!isRecord(value)) return "";
  return Object.entries(value)
    .map(([key, item]) => `${key}=${typeof item === "string" ? item : JSON.stringify(item)}`)
    .join("\n");
}

/** A field's draft for its current value (absent: empty, or a boolean's default). */
export function fieldDraft(field: ConfigFormField, value: unknown): ConfigFieldDraft {
  switch (field.kind) {
    case "boolean":
      return typeof value === "boolean" ? value : field.default === true;
    case "string":
    case "enum":
      return typeof value === "string" ? value : value === undefined ? "" : JSON.stringify(value);
    case "number":
    case "integer":
      return typeof value === "number" ? String(value) : typeof value === "string" ? value : "";
    case "stringList":
      return Array.isArray(value) ? value.map((item) => (typeof item === "string" ? item : JSON.stringify(item))).join("\n") : "";
    case "stringMap":
      return stringMapText(value);
    default:
      return value === undefined ? "" : JSON.stringify(value, null, 2);
  }
}

export function draftsFor(fields: readonly ConfigFormField[], values: Record<string, unknown>): ConfigFieldDrafts {
  return Object.fromEntries(fields.map((field) => [field.key, fieldDraft(field, values[field.key])]));
}

function isBlank(draft: ConfigFieldDraft | undefined): boolean {
  return typeof draft !== "boolean" && (draft ?? "").trim() === "";
}

const PLAIN_INTEGER = /^[+-]?\d+$/;
const PLAIN_DECIMAL = /^[+-]?(?:\d+\.?\d*|\.\d+)$/;

/** The `KEY=VALUE` lines of a map draft (keys trimmed), or the error message key. */
function mapEntries(text: string): Array<[string, string]> | string {
  const entries: Array<[string, string]> = [];
  const seen = new Set<string>();
  for (const line of lines(text)) {
    if (!line.trim()) continue;
    const index = line.indexOf("=");
    const key = index < 0 ? "" : line.slice(0, index).trim();
    if (!key) return "debug.configDialog.invalidMapLine";
    if (seen.has(key)) return "debug.configDialog.duplicateMapKey";
    seen.add(key);
    entries.push([key, line.slice(index + 1)]);
  }
  return entries;
}

function numberError(field: ConfigFormField, text: string): string | undefined {
  const integer = field.kind === "integer";
  if (!(integer ? PLAIN_INTEGER : PLAIN_DECIMAL).test(text)) return integer ? "debug.configDialog.invalidInteger" : "debug.configDialog.invalidNumber";
  if (field.key !== "port") return undefined;
  const port = Number(text);
  return Number.isInteger(port) && port >= 1 && port <= 65535 ? undefined : "debug.configDialog.invalidPort";
}

/** The error message key of one draft, or undefined when it is valid. */
export function validateConfigField(field: ConfigFormField, draft: ConfigFieldDraft | undefined): string | undefined {
  if (isBlank(draft)) return field.required ? "debug.configDialog.required" : undefined;
  if (typeof draft === "boolean") return undefined;
  const text = draft ?? "";
  switch (field.kind) {
    case "number":
    case "integer":
      return numberError(field, text.trim());
    case "stringMap": {
      const entries = mapEntries(text);
      return typeof entries === "string" ? entries : undefined;
    }
    case "json":
      try {
        JSON.parse(text);
        return undefined;
      } catch {
        return "debug.configDialog.invalidJson";
      }
    default:
      return undefined;
  }
}

/** Error message keys by field key: required, number, port, `KEY=VALUE` and JSON errors. */
export function validateConfigFields(fields: readonly ConfigFormField[], values: ConfigFieldDrafts): Record<string, string> {
  const errors: Record<string, string> = {};
  for (const field of fields) {
    const error = validateConfigField(field, values[field.key]);
    if (error) errors[field.key] = error;
  }
  return errors;
}

/** The value a valid draft stands for; undefined removes the field. */
function draftValue(field: ConfigFormField, draft: ConfigFieldDraft | undefined, previous: unknown): unknown {
  if (typeof draft === "boolean") {
    // Leaving an unset boolean at its default keeps it unset.
    return previous !== undefined || draft !== (field.default === true) ? draft : undefined;
  }
  if (isBlank(draft)) return undefined;
  const text = draft ?? "";
  switch (field.kind) {
    case "number":
    case "integer":
      return Number(text.trim());
    case "stringList":
      return lines(text).filter((line) => line !== "");
    case "stringMap": {
      const entries = mapEntries(text);
      return typeof entries === "string" ? {} : Object.fromEntries(entries);
    }
    case "json":
      return JSON.parse(text) as unknown;
    default:
      return text;
  }
}

/**
 * The configuration fields after applying valid drafts to base: fields the
 * form does not show are kept, emptied ones removed. Validate first.
 */
export function applyConfigFields(fields: readonly ConfigFormField[], drafts: ConfigFieldDrafts, base: Record<string, unknown>): Record<string, unknown> {
  const result: Record<string, unknown> = JSON.parse(JSON.stringify(base)) as Record<string, unknown>;
  for (const field of fields) {
    const value = draftValue(field, drafts[field.key], base[field.key]);
    if (value === undefined) delete result[field.key];
    else result[field.key] = value;
  }
  return result;
}

/** The debuggee's server-side arguments: argsTemplate with `${port}` replaced (kept while the port is empty). */
export function remoteServerArgs(template: string, port: string | number | undefined): string {
  const value = port === undefined ? "" : String(port).trim();
  return value ? template.split("${port}").join(value) : template;
}

function schemaKeys(schema: JsonSchemaObject | undefined): Set<string> {
  return new Set(isRecord(schema?.properties) ? Object.keys(schema.properties) : []);
}

/**
 * Switching a configuration between launch and attach. Fields only the old
 * request describes are put aside in the stash (by key) and leave the
 * configuration; fields only the new request describes come back from the
 * stash. Shared and undescribed fields stay as they are.
 */
export function switchRequestFields(
  fields: Record<string, unknown>,
  from: JsonSchemaObject | undefined,
  to: JsonSchemaObject | undefined,
  stash: Record<string, unknown>,
): { fields: Record<string, unknown>; stash: Record<string, unknown> } {
  const fromKeys = schemaKeys(from);
  const toKeys = schemaKeys(to);
  const next: Record<string, unknown> = {};
  const nextStash: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(stash)) if (!toKeys.has(key) || fromKeys.has(key)) nextStash[key] = value;
  for (const [key, value] of Object.entries(fields)) {
    if (fromKeys.has(key) && !toKeys.has(key)) nextStash[key] = value;
    else next[key] = value;
  }
  for (const [key, value] of Object.entries(stash)) {
    if (toKeys.has(key) && !fromKeys.has(key) && !(key in next)) next[key] = value;
  }
  return { fields: next, stash: nextStash };
}
