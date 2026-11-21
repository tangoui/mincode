/**
 * Command inputSchema helpers for the host-rendered parameter form. Mirrors
 * the restricted dialect validated in backend/plugin/command_context.go:
 * an object schema with scalar/enum properties only.
 */

export type CommandInputPropertyType = "string" | "integer" | "number" | "boolean";

export interface CommandInputProperty {
  name: string;
  type: CommandInputPropertyType;
  description?: string;
  enum?: (string | number | boolean)[];
  default?: string | number | boolean;
  required: boolean;
}

export interface CommandInputForm {
  description?: string;
  properties: CommandInputProperty[];
}

const PROPERTY_TYPES: readonly CommandInputPropertyType[] = ["string", "integer", "number", "boolean"];
const MAX_PROPERTIES = 32;
const MAX_ENUM_VALUES = 32;

function isScalar(value: unknown): value is string | number | boolean {
  return typeof value === "string" || typeof value === "number" || typeof value === "boolean";
}

function scalarMatchesType(value: unknown, type: CommandInputPropertyType): boolean {
  switch (type) {
    case "string":
      return typeof value === "string";
    case "boolean":
      return typeof value === "boolean";
    case "integer":
      return typeof value === "number" && Number.isInteger(value);
    case "number":
      return typeof value === "number";
  }
}

/**
 * Parses a backend-delivered inputSchema into a renderable form model.
 * Returns null for anything outside the form-renderable subset so callers
 * can fall back to invoking without parameters.
 */
export function parseCommandInputForm(schema: Record<string, unknown>): CommandInputForm | null {
  if (schema.type !== "object") return null;
  const rawProperties = schema.properties;
  if (typeof rawProperties !== "object" || rawProperties === null || Array.isArray(rawProperties)) return null;
  const entries = Object.entries(rawProperties as Record<string, unknown>);
  if (entries.length === 0 || entries.length > MAX_PROPERTIES) return null;
  const required = new Set<string>();
  if (Array.isArray(schema.required)) {
    for (const entry of schema.required) {
      if (typeof entry !== "string") return null;
      required.add(entry);
    }
  }
  const properties: CommandInputProperty[] = [];
  for (const [name, raw] of entries) {
    if (typeof raw !== "object" || raw === null || Array.isArray(raw)) return null;
    const candidate = raw as Record<string, unknown>;
    if (typeof candidate.type !== "string" || !PROPERTY_TYPES.includes(candidate.type as CommandInputPropertyType)) {
      return null;
    }
    const type = candidate.type as CommandInputPropertyType;
    const property: CommandInputProperty = { name, type, required: required.has(name) };
    if (candidate.description !== undefined) {
      if (typeof candidate.description !== "string") return null;
      property.description = candidate.description;
    }
    if (candidate.enum !== undefined) {
      if (!Array.isArray(candidate.enum) || candidate.enum.length === 0 || candidate.enum.length > MAX_ENUM_VALUES) {
        return null;
      }
      if (!candidate.enum.every((value) => isScalar(value) && scalarMatchesType(value, type))) return null;
      property.enum = candidate.enum;
    }
    if (candidate.default !== undefined) {
      if (!isScalar(candidate.default) || !scalarMatchesType(candidate.default, type)) return null;
      property.default = candidate.default;
    }
    properties.push(property);
  }
  const form: CommandInputForm = { properties };
  if (typeof schema.description === "string") form.description = schema.description;
  return form;
}

/** Initial form values from declared defaults; required scalars get placeholders. */
export function commandInputInitialValues(form: CommandInputForm): Record<string, string | number | boolean | undefined> {
  const values: Record<string, string | number | boolean | undefined> = {};
  for (const property of form.properties) {
    if (property.default !== undefined) {
      values[property.name] = property.default;
    } else if (property.type === "boolean") {
      values[property.name] = false;
    } else {
      values[property.name] = undefined;
    }
  }
  return values;
}

/**
 * Validates filled values against the form model. Returns an error message
 * key (i18n under commandPalette.pluginParams) or null when valid.
 */
export function validateCommandInputValues(
  form: CommandInputForm,
  values: Record<string, string | number | boolean | undefined>,
): { property: string; reason: "required" | "type" } | null {
  for (const property of form.properties) {
    const value = values[property.name];
    const missing = value === undefined || (property.type === "string" && value === "");
    if (missing) {
      if (property.required) return { property: property.name, reason: "required" };
      continue;
    }
    if (!scalarMatchesType(value, property.type)) return { property: property.name, reason: "type" };
  }
  return null;
}

/** Serializes filled values into the JSON payload sent to plugin.invoke. */
export function serializeCommandInputValues(
  form: CommandInputForm,
  values: Record<string, string | number | boolean | undefined>,
): string {
  const payload: Record<string, string | number | boolean> = {};
  for (const property of form.properties) {
    const value = values[property.name];
    if (value === undefined || (property.type === "string" && value === "")) continue;
    payload[property.name] = value;
  }
  return JSON.stringify(payload);
}
