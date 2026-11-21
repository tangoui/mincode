/**
 * Pure model of plugin settings (`contributes.configuration`).
 *
 * The Go host validates schemas and values (backend/plugin/
 * contrib_configuration.go, configuration_store.go); this module re-checks the
 * native payloads at the service boundary, turns a schema into ordered form
 * fields, and owns the configuration-sync entry shape. Secret settings never
 * carry values here: the host reports only whether each one is set.
 */
import { resolvePluginTitle } from "./title";

export type PluginConfigurationScope = "global" | "workspace";
export type PluginConfigurationControl = "string" | "secret" | "number" | "integer" | "boolean" | "enum" | "array";
export type PluginConfigurationValue = string | number | boolean | string[];
export type PluginConfigurationValues = Record<string, PluginConfigurationValue>;

export interface PluginConfigurationProperty {
  type: "string" | "number" | "integer" | "boolean" | "array";
  title?: string;
  titleLocalized?: Record<string, string>;
  description?: string;
  descriptionLocalized?: Record<string, string>;
  default?: PluginConfigurationValue;
  enum?: Array<string | number>;
  enumLabelsLocalized?: Record<string, string[]>;
  format?: "url" | "path";
  minimum?: number;
  maximum?: number;
  maxLength?: number;
  items?: { type: "string"; enum?: string[]; maxLength?: number };
  maxItems?: number;
  secret?: boolean;
  scope: PluginConfigurationScope;
  order?: number;
}

export interface PluginConfigurationSchema {
  title?: string;
  titleLocalized?: Record<string, string>;
  properties: Record<string, PluginConfigurationProperty>;
}

export interface PluginConfigurationIgnoredValue {
  scope: PluginConfigurationScope;
  workspaceId?: string;
  key: string;
  reason: string;
}

export interface PluginConfigurationState {
  pluginId: string;
  schema: PluginConfigurationSchema;
  workspaceId?: string;
  global: PluginConfigurationValues;
  workspace: PluginConfigurationValues;
  effective: PluginConfigurationValues;
  /** Whether each declared secret setting currently has a stored value. */
  secrets: Record<string, boolean>;
  /** Stored secrets the current schema no longer declares. */
  orphanSecrets: string[];
  ignored: PluginConfigurationIgnoredValue[];
  storeError?: string;
  secretsAvailable: boolean;
  secretsReadFailed: boolean;
}

export interface PluginConfigurationOption {
  value: string | number;
  label: string;
}

export interface PluginConfigurationField {
  key: string;
  control: PluginConfigurationControl;
  title: string;
  description: string;
  scope: PluginConfigurationScope;
  property: PluginConfigurationProperty;
  options: PluginConfigurationOption[];
}

const MAX_PROPERTIES = 64;
const MAX_LIST = 256;
const KEY_PATTERN = /^[a-z][a-zA-Z0-9]*(\.[a-z][a-zA-Z0-9]*){0,3}$/;

type JsonRecord = Record<string, unknown>;

function record(value: unknown): JsonRecord | undefined {
  return value && typeof value === "object" && !Array.isArray(value) ? value as JsonRecord : undefined;
}

function text(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}

function finite(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function localizedMap(value: unknown): Record<string, string> | undefined {
  const raw = record(value);
  if (!raw) return undefined;
  const result: Record<string, string> = {};
  for (const [locale, entry] of Object.entries(raw).slice(0, 16)) {
    if (typeof entry === "string" && entry.trim()) result[locale] = entry;
  }
  return Object.keys(result).length ? result : undefined;
}

function stringList(value: unknown, max = MAX_LIST): string[] | undefined {
  if (!Array.isArray(value) || value.length > max || value.some((item) => typeof item !== "string")) return undefined;
  return value as string[];
}

/** Checks one value against its property (mirrors validateConfigurationValue in Go, minus URL parsing details). */
export function isValidConfigurationValue(property: PluginConfigurationProperty, value: unknown): value is PluginConfigurationValue {
  switch (property.type) {
    case "boolean":
      return typeof value === "boolean";
    case "array": {
      const list = stringList(value, property.maxItems || MAX_LIST);
      if (!list) return false;
      const allowed = property.items?.enum;
      const maxLength = property.items?.maxLength || 16 * 1024;
      return list.every((item) => [...item].length <= maxLength && (!allowed?.length || allowed.includes(item)));
    }
    case "string":
      if (typeof value !== "string" || [...value].length > (property.maxLength || 16 * 1024)) return false;
      break;
    case "number":
    case "integer":
      if (finite(value) === undefined) return false;
      if (property.type === "integer" && !Number.isSafeInteger(value)) return false;
      if (property.minimum !== undefined && (value as number) < property.minimum) return false;
      if (property.maximum !== undefined && (value as number) > property.maximum) return false;
      break;
    default:
      return false;
  }
  return !property.enum?.length || property.enum.includes(value as string | number);
}

function parseProperty(value: unknown): PluginConfigurationProperty | undefined {
  const raw = record(value);
  const type = raw?.type;
  if (!raw || (type !== "string" && type !== "number" && type !== "integer" && type !== "boolean" && type !== "array")) return undefined;
  const property: PluginConfigurationProperty = { type, scope: raw.scope === "workspace" ? "workspace" : "global" };
  property.title = text(raw.title);
  property.titleLocalized = localizedMap(raw.titleLocalized);
  property.description = text(raw.description);
  property.descriptionLocalized = localizedMap(raw.descriptionLocalized);
  if (raw.format === "url" || raw.format === "path") property.format = raw.format;
  property.minimum = finite(raw.minimum);
  property.maximum = finite(raw.maximum);
  property.maxLength = finite(raw.maxLength);
  property.maxItems = finite(raw.maxItems);
  property.order = finite(raw.order);
  property.secret = raw.secret === true && type === "string";
  if (Array.isArray(raw.enum) && raw.enum.length <= 64 && raw.enum.every((item) => typeof item === "string" || finite(item) !== undefined)) {
    property.enum = raw.enum as Array<string | number>;
  }
  const labels = record(raw.enumLabelsLocalized);
  if (labels && property.enum) {
    const parsed: Record<string, string[]> = {};
    for (const [locale, list] of Object.entries(labels).slice(0, 16)) {
      const items = stringList(list);
      if (items && items.length === property.enum.length) parsed[locale] = items;
    }
    property.enumLabelsLocalized = parsed;
  }
  const items = record(raw.items);
  if (type === "array") {
    property.items = { type: "string", enum: stringList(items?.enum, 64), maxLength: finite(items?.maxLength) };
  }
  if (raw.default !== undefined && !property.secret && isValidConfigurationValue(property, raw.default)) property.default = raw.default;
  return property;
}

export function parseConfigurationSchema(value: unknown): PluginConfigurationSchema | undefined {
  const raw = record(value);
  const properties = record(raw?.properties);
  if (!raw || !properties) return undefined;
  const schema: PluginConfigurationSchema = { title: text(raw.title), titleLocalized: localizedMap(raw.titleLocalized), properties: {} };
  for (const [key, entry] of Object.entries(properties).slice(0, MAX_PROPERTIES)) {
    if (key.length > 64 || !KEY_PATTERN.test(key)) continue;
    const property = parseProperty(entry);
    if (property) schema.properties[key] = property;
  }
  return Object.keys(schema.properties).length ? schema : undefined;
}

/** Keeps only declared, valid, non-secret values of a scope. */
export function sanitizeConfigurationValues(
  schema: PluginConfigurationSchema,
  value: unknown,
  scope: PluginConfigurationScope = "global",
): PluginConfigurationValues {
  const raw = record(value);
  const result: PluginConfigurationValues = {};
  if (!raw) return result;
  for (const [key, entry] of Object.entries(raw)) {
    const property = Object.prototype.hasOwnProperty.call(schema.properties, key) ? schema.properties[key] : undefined;
    if (!property || property.secret) continue;
    if (scope === "workspace" && property.scope !== "workspace") continue;
    if (isValidConfigurationValue(property, entry)) result[key] = entry;
  }
  return result;
}

function parseIgnored(value: unknown): PluginConfigurationIgnoredValue[] {
  if (!Array.isArray(value)) return [];
  return value.slice(0, 1024).flatMap((item) => {
    const raw = record(item);
    const key = text(raw?.key);
    if (!raw || !key || key.length > 256) return [];
    const scope: PluginConfigurationScope = raw.scope === "workspace" ? "workspace" : "global";
    const workspaceId = text(raw.workspaceId);
    return [{ scope, ...(workspaceId ? { workspaceId } : {}), key, reason: text(raw.reason) ?? "" }];
  });
}

/** Validates the `plugin_host_configuration_get` payload. */
export function parseConfigurationState(value: unknown): PluginConfigurationState | undefined {
  const raw = record(value);
  const pluginId = text(raw?.pluginId);
  const schema = parseConfigurationSchema(raw?.schema);
  if (!raw || !pluginId || !schema) return undefined;
  const secrets: Record<string, boolean> = {};
  const rawSecrets = record(raw.secrets) ?? {};
  for (const [key, property] of Object.entries(schema.properties)) {
    if (property.secret) secrets[key] = rawSecrets[key] === true;
  }
  const workspaceId = text(raw.workspaceId);
  return {
    pluginId,
    schema,
    ...(workspaceId ? { workspaceId } : {}),
    global: sanitizeConfigurationValues(schema, raw.global),
    workspace: sanitizeConfigurationValues(schema, raw.workspace, "workspace"),
    effective: sanitizeConfigurationValues(schema, raw.effective),
    secrets,
    orphanSecrets: (stringList(raw.orphanSecrets) ?? []).filter((key) => KEY_PATTERN.test(key)),
    ignored: parseIgnored(raw.ignored),
    ...(text(raw.storeError) ? { storeError: text(raw.storeError) } : {}),
    secretsAvailable: raw.secretsAvailable === true,
    secretsReadFailed: raw.secretsReadFailed === true,
  };
}

/** Plugin-owned text for a locale tag or chain (`useVueI18n().localeChain`); language packs never apply to settings. */
function localizedText(base: string | undefined, localized: Record<string, string> | undefined, locale: string | readonly string[]): string {
  return resolvePluginTitle({ title: base?.trim() ?? "", titleLocalized: localized }, locale);
}

/** Enum labels for the first tag in the chain (tag, then its bare language) that the plugin localized. */
function localizedEnumLabels(labels: Record<string, string[]> | undefined, locale: string | readonly string[]): string[] | undefined {
  if (!labels) return undefined;
  for (const tag of typeof locale === "string" ? [locale] : locale) {
    for (const key of [tag, tag.split("-")[0] ?? ""]) {
      if (key && Object.prototype.hasOwnProperty.call(labels, key)) return labels[key];
    }
  }
  return undefined;
}

function fieldControl(property: PluginConfigurationProperty): PluginConfigurationControl {
  if (property.secret) return "secret";
  if (property.enum?.length) return "enum";
  return property.type;
}

/** Orders a schema into form fields: `order` ascending, then key. */
export function configurationFields(schema: PluginConfigurationSchema, locale: string | readonly string[]): PluginConfigurationField[] {
  return Object.entries(schema.properties)
    .sort(([leftKey, left], [rightKey, right]) => (left.order ?? 0) - (right.order ?? 0) || leftKey.localeCompare(rightKey))
    .map(([key, property]) => {
      const labels = localizedEnumLabels(property.enumLabelsLocalized, locale);
      const values: Array<string | number> = property.enum ?? property.items?.enum ?? [];
      return {
        key,
        control: fieldControl(property),
        title: localizedText(property.title, property.titleLocalized, locale) || key,
        description: localizedText(property.description, property.descriptionLocalized, locale),
        scope: property.scope,
        property,
        options: values.map((value, index) => ({ value, label: labels?.[index] ?? String(value) })),
      };
    });
}

/** Value shown for a field: the scope's own value, else the inherited effective value, else a type-neutral empty value. */
export function configurationFieldValue(field: PluginConfigurationField, own: PluginConfigurationValues, inherited: PluginConfigurationValues): PluginConfigurationValue {
  if (Object.prototype.hasOwnProperty.call(own, field.key)) return own[field.key]!;
  if (Object.prototype.hasOwnProperty.call(inherited, field.key)) return inherited[field.key]!;
  switch (field.property.type) {
    case "boolean":
      return false;
    case "array":
      return [];
    case "number":
    case "integer":
      return field.property.minimum ?? 0;
    default:
      return "";
  }
}

/**
 * Values an embedded plugin page (iframe bridge) may receive: declared,
 * valid, non-secret values only. Secret settings reach the plugin process
 * through host.configuration.get, never the sandboxed UI.
 */
export function bridgeConfigurationValues(state: PluginConfigurationState, keys?: readonly string[]): PluginConfigurationValues {
  const values = sanitizeConfigurationValues(state.schema, state.effective);
  if (!keys) return values;
  const result: PluginConfigurationValues = {};
  for (const key of keys) {
    if (Object.prototype.hasOwnProperty.call(values, key)) result[key] = values[key]!;
  }
  return result;
}

// ---- configuration sync ----

/**
 * Synthetic config-sync entry. Only global, non-secret values of installed
 * plugins are synced; secrets and workspace overrides never leave the device.
 * It is applied through the Go host, never persisted in a frontend store.
 */
export const PLUGIN_CONFIGURATION_SYNC_KEY = "app_plugin_configuration_v1";
const SYNC_MAX_PLUGINS = 512;
const PLUGIN_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;

export interface PluginConfigurationSyncData {
  version: 1;
  plugins: Record<string, { global: Record<string, PluginConfigurationValue> }>;
}

function syncValue(value: unknown): value is PluginConfigurationValue {
  return typeof value === "string" || typeof value === "boolean" || finite(value) !== undefined
    || stringList(value) !== undefined;
}

/** Validates a sync payload (from the host or from the cloud); invalid entries are dropped. */
export function parsePluginConfigurationSync(value: unknown): PluginConfigurationSyncData | undefined {
  const raw = record(value);
  const plugins = record(raw?.plugins);
  if (!raw || raw.version !== 1 || !plugins) return undefined;
  const result: PluginConfigurationSyncData = { version: 1, plugins: {} };
  for (const [id, entry] of Object.entries(plugins).slice(0, SYNC_MAX_PLUGINS)) {
    const global = record(record(entry)?.global);
    if (!PLUGIN_ID_PATTERN.test(id) || id === "__host__" || !global) continue;
    const values: Record<string, PluginConfigurationValue> = {};
    for (const [key, item] of Object.entries(global).slice(0, MAX_PROPERTIES)) {
      if (KEY_PATTERN.test(key) && syncValue(item)) values[key] = item;
    }
    if (Object.keys(values).length) result.plugins[id] = { global: values };
  }
  return result;
}

/** The sync entry to include in a snapshot, or undefined when there is nothing to sync. */
export function pluginConfigurationSyncEntry(value: unknown): { storage: "plain"; value: PluginConfigurationSyncData } | undefined {
  const data = parsePluginConfigurationSync(value);
  return data && Object.keys(data.plugins).length ? { storage: "plain", value: data } : undefined;
}
