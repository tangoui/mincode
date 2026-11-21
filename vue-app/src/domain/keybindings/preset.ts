/**
 * Keymap preset model, validator and registry.
 *
 * The keymap engine and the MinCode preset are built into the host. Every
 * other preset (VS Code, IntelliJ IDEA, Eclipse, Vim, third-party) is plain
 * declarative data contributed by a declarative plugin through the manifest
 * `contributes.keymaps` point, so every preset — built-in or plugin — goes
 * through `validateKeymapPreset` before `registerKeymapPreset` accepts it.
 */
import { parseWhenExpression } from "@/domain/commands/when";
import { isBindableCommandId, KEYMAP_COMMAND_MAX_LENGTH, KEYMAP_COMMAND_PATTERN } from "@/domain/keybindings/catalog";
import { parseKeySequence, type KeyboardPlatform } from "@/domain/keybindings/keys";
import { reservedKeyReason } from "@/domain/keybindings/reserved";

export const KEYBOARD_PLATFORMS: readonly KeyboardPlatform[] = ["mac", "win", "linux"];
export const MINCODE_PRESET_ID = "mincode";

export const KEYMAP_PRESET_LIMITS = Object.freeze({
  maxRules: 1000,
  maxTitleLength: 64,
  maxDescriptionLength: 256,
  maxSerializedBytes: 256 * 1024,
  maxExtendsDepth: 4,
});

export type KeymapEditorMode = "vim";
export type KeymapPresetSource = "builtin" | "plugin";

/** One rule of a preset. `command: "-<id>"` removes an inherited binding. */
export interface KeymapPresetRule {
  key?: string;
  command: string;
  when?: string;
  /** Restricts the rule to one or more platforms; omitted = every platform. */
  platform?: KeyboardPlatform | KeyboardPlatform[];
}

export interface KeymapPresetTitle {
  zh: string;
  en: string;
}

/** External (plugin-contributable) preset data, as validated JSON. */
export interface KeymapPresetData {
  id: string;
  title: KeymapPresetTitle;
  description?: KeymapPresetTitle;
  extends?: string;
  /** Default for the double-modifier option (e.g. JetBrains Shift Shift). */
  doubleModifier?: boolean;
  /** Modal editor emulation requested by the preset (implemented by an editor-mode provider). */
  editorMode?: KeymapEditorMode;
  rules: KeymapPresetRule[];
}

export interface KeymapPreset extends KeymapPresetData {
  source: KeymapPresetSource;
  /** Owning plugin for `source: "plugin"`. */
  pluginId?: string;
  /** Display name of the owning plugin (settings shows it as the preset source). */
  pluginName?: string;
}

/** A preset rule narrowed to one platform, ready for the resolver. */
export interface KeybindingRule {
  key?: string;
  command: string;
  when?: string;
}

export interface KeymapValidationIssue {
  path: string;
  message: string;
}

export type KeymapValidationResult =
  | { ok: true; preset: KeymapPresetData }
  | { ok: false; errors: KeymapValidationIssue[] };

const PRESET_ID_PATTERN = /^[a-z0-9][a-z0-9._-]{0,63}$/;
const TOP_LEVEL_FIELDS = new Set(["id", "title", "description", "extends", "doubleModifier", "editorMode", "rules"]);
const RULE_FIELDS = new Set(["key", "command", "when", "platform"]);

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function validateTitle(value: unknown, path: string, errors: KeymapValidationIssue[], required: boolean) {
  if (value === undefined && !required) return undefined;
  if (!isRecord(value)) {
    errors.push({ path, message: "must be an object with zh and en strings" });
    return undefined;
  }
  const title: Partial<KeymapPresetTitle> = {};
  const limit = path.endsWith("title") ? KEYMAP_PRESET_LIMITS.maxTitleLength : KEYMAP_PRESET_LIMITS.maxDescriptionLength;
  for (const locale of ["zh", "en"] as const) {
    const text = value[locale];
    if (typeof text !== "string" || !text.trim() || text.length > limit) {
      errors.push({ path: `${path}.${locale}`, message: `must be a non-empty string of at most ${limit} characters` });
    } else {
      title[locale] = text.trim();
    }
  }
  return title.zh && title.en ? (title as KeymapPresetTitle) : undefined;
}

function rulePlatforms(platform: KeymapPresetRule["platform"]): KeyboardPlatform[] {
  if (platform === undefined) return [...KEYBOARD_PLATFORMS];
  return Array.isArray(platform) ? platform : [platform];
}

function validateRule(value: unknown, path: string, errors: KeymapValidationIssue[]): KeymapPresetRule | undefined {
  if (!isRecord(value)) {
    errors.push({ path, message: "must be an object" });
    return undefined;
  }
  for (const field of Object.keys(value)) {
    if (!RULE_FIELDS.has(field)) errors.push({ path: `${path}.${field}`, message: "is not a supported field" });
  }
  const before = errors.length;
  const command = value.command;
  const removal = typeof command === "string" && command.startsWith("-");
  const commandId = typeof command === "string" ? (removal ? command.slice(1) : command) : "";
  if (
    typeof command !== "string" ||
    command.length > KEYMAP_COMMAND_MAX_LENGTH ||
    !KEYMAP_COMMAND_PATTERN.test(command) ||
    !isBindableCommandId(commandId)
  ) {
    errors.push({ path: `${path}.command`, message: `must be a catalog or plugin:* command id, got ${JSON.stringify(command)}` });
  }
  let platforms: KeyboardPlatform[] = [...KEYBOARD_PLATFORMS];
  if (value.platform !== undefined) {
    const list = Array.isArray(value.platform) ? value.platform : [value.platform];
    if (!list.length || list.some((item) => !KEYBOARD_PLATFORMS.includes(item as KeyboardPlatform)) || new Set(list).size !== list.length) {
      errors.push({ path: `${path}.platform`, message: "must be mac, win, linux or a list of them" });
    } else {
      platforms = list as KeyboardPlatform[];
    }
  }
  const key = value.key;
  if (key === undefined) {
    if (!removal) errors.push({ path: `${path}.key`, message: "is required unless the rule removes a command" });
  } else if (typeof key !== "string") {
    errors.push({ path: `${path}.key`, message: "must be a string" });
  } else {
    for (const platform of platforms) {
      const sequence = parseKeySequence(key, platform);
      if (!sequence) {
        errors.push({ path: `${path}.key`, message: `is not a valid key sequence: ${JSON.stringify(key)}` });
        break;
      }
      const reserved = reservedKeyReason(sequence, platform);
      if (!removal && reserved?.severity === "reserved") {
        errors.push({ path: `${path}.key`, message: `${JSON.stringify(key)} is reserved by the operating system on ${platform}` });
      }
    }
  }
  const when = value.when;
  if (when !== undefined) {
    if (typeof when !== "string") {
      errors.push({ path: `${path}.when`, message: "must be a string" });
    } else {
      try {
        parseWhenExpression(when);
      } catch (error) {
        errors.push({ path: `${path}.when`, message: error instanceof Error ? error.message : String(error) });
      }
    }
  }
  if (errors.length !== before) return undefined;
  const rule: KeymapPresetRule = { command: command as string };
  if (typeof key === "string") rule.key = key.trim();
  if (typeof when === "string") rule.when = when.trim();
  if (value.platform !== undefined) rule.platform = value.platform as KeymapPresetRule["platform"];
  return rule;
}

/**
 * Validates untrusted preset data (plugin manifests, imported files). Commands
 * must be catalog or plugin:* ids, keys must parse on every platform the rule
 * applies to and must not be reserved by the OS, when-clauses must parse, and
 * the rule count and serialized size are bounded.
 */
export function validateKeymapPreset(input: unknown): KeymapValidationResult {
  const errors: KeymapValidationIssue[] = [];
  if (!isRecord(input)) return { ok: false, errors: [{ path: "", message: "preset must be an object" }] };
  let serialized: string;
  try {
    serialized = JSON.stringify(input);
  } catch {
    return { ok: false, errors: [{ path: "", message: "preset must be JSON-serializable" }] };
  }
  if (new TextEncoder().encode(serialized).length > KEYMAP_PRESET_LIMITS.maxSerializedBytes) {
    return { ok: false, errors: [{ path: "", message: `preset exceeds ${KEYMAP_PRESET_LIMITS.maxSerializedBytes} bytes` }] };
  }
  for (const field of Object.keys(input)) {
    if (!TOP_LEVEL_FIELDS.has(field)) errors.push({ path: field, message: "is not a supported field" });
  }
  const id = input.id;
  if (typeof id !== "string" || !PRESET_ID_PATTERN.test(id)) {
    errors.push({ path: "id", message: "must match [a-z0-9][a-z0-9._-]{0,63}" });
  }
  const title = validateTitle(input.title, "title", errors, true);
  const description = validateTitle(input.description, "description", errors, false);
  if (input.extends !== undefined && (typeof input.extends !== "string" || !PRESET_ID_PATTERN.test(input.extends) || input.extends === id)) {
    errors.push({ path: "extends", message: "must be another preset id" });
  }
  if (input.doubleModifier !== undefined && typeof input.doubleModifier !== "boolean") {
    errors.push({ path: "doubleModifier", message: "must be a boolean" });
  }
  if (input.editorMode !== undefined && input.editorMode !== "vim") {
    errors.push({ path: "editorMode", message: 'must be "vim" when present' });
  }
  const rules: KeymapPresetRule[] = [];
  if (!Array.isArray(input.rules)) {
    errors.push({ path: "rules", message: "must be an array" });
  } else if (input.rules.length > KEYMAP_PRESET_LIMITS.maxRules) {
    errors.push({ path: "rules", message: `must contain at most ${KEYMAP_PRESET_LIMITS.maxRules} rules` });
  } else {
    input.rules.forEach((value, index) => {
      const rule = validateRule(value, `rules[${index}]`, errors);
      if (rule) rules.push(rule);
    });
  }
  if (errors.length || !title) return { ok: false, errors };
  const preset: KeymapPresetData = { id: id as string, title, rules };
  if (description) preset.description = description;
  if (typeof input.extends === "string") preset.extends = input.extends;
  if (typeof input.doubleModifier === "boolean") preset.doubleModifier = input.doubleModifier;
  if (input.editorMode === "vim") preset.editorMode = "vim";
  return { ok: true, preset };
}

/** Narrows preset rules to one platform. */
export function presetRulesForPlatform(rules: readonly KeymapPresetRule[], platform: KeyboardPlatform): KeybindingRule[] {
  const result: KeybindingRule[] = [];
  for (const rule of rules) {
    if (!rulePlatforms(rule.platform).includes(platform)) continue;
    const narrowed: KeybindingRule = { command: rule.command };
    if (rule.key !== undefined) narrowed.key = rule.key;
    if (rule.when !== undefined) narrowed.when = rule.when;
    result.push(narrowed);
  }
  return result;
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

const presets = new Map<string, { token: symbol; preset: KeymapPreset }>();
const listeners = new Set<() => void>();

function notify() {
  for (const listener of listeners) listener();
}

export class KeymapPresetError extends Error {
  readonly issues: KeymapValidationIssue[];
  constructor(message: string, issues: KeymapValidationIssue[]) {
    super(message);
    this.name = "KeymapPresetError";
    this.issues = issues;
  }
}

/**
 * Registers a preset after validation. Returns a disposer that removes it
 * again (used when a keymap plugin is disabled or uninstalled). A plugin may
 * not replace a built-in preset id.
 */
export function registerKeymapPreset(
  data: unknown,
  options: { source: KeymapPresetSource; pluginId?: string; pluginName?: string },
): () => void {
  const result = validateKeymapPreset(data);
  if (!result.ok) {
    throw new KeymapPresetError(`Invalid keymap preset: ${result.errors.map((issue) => `${issue.path} ${issue.message}`).join("; ")}`, result.errors);
  }
  if (options.source === "plugin") {
    // Mirrors the Go manifest validator: a plugin owns the `<pluginId>.` id
    // namespace and may extend only MinCode or its own presets.
    const prefix = `${options.pluginId ?? ""}.`;
    if (!options.pluginId || !result.preset.id.startsWith(prefix) || result.preset.id.length === prefix.length) {
      throw new KeymapPresetError(`Keymap preset ${result.preset.id} must start with ${JSON.stringify(prefix)}`, []);
    }
    const parent = result.preset.extends;
    if (parent !== undefined && parent !== MINCODE_PRESET_ID && !parent.startsWith(prefix)) {
      throw new KeymapPresetError(`Keymap preset ${result.preset.id} may only extend ${MINCODE_PRESET_ID} or a preset of the same plugin`, []);
    }
  }
  const existing = presets.get(result.preset.id);
  if (existing && existing.preset.source === "builtin" && options.source !== "builtin") {
    throw new KeymapPresetError(`Keymap preset ${result.preset.id} is built in`, []);
  }
  const token = Symbol(result.preset.id);
  const preset: KeymapPreset = { ...result.preset, source: options.source };
  if (options.pluginId) preset.pluginId = options.pluginId;
  if (options.pluginName?.trim()) preset.pluginName = options.pluginName.trim();
  presets.set(preset.id, { token, preset });
  notify();
  return () => {
    if (presets.get(preset.id)?.token !== token) return;
    presets.delete(preset.id);
    notify();
  };
}

export function listKeymapPresets(): KeymapPreset[] {
  return [...presets.values()].map(({ preset }) => preset);
}

export function getKeymapPreset(id: string): KeymapPreset | undefined {
  return presets.get(id)?.preset;
}

/** Notifies when presets are registered or removed; returns an unsubscribe function. */
export function subscribeKeymapPresets(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/**
 * The preset and its `extends` ancestors, root first. Returns null when the
 * preset or an ancestor is missing, or the chain is cyclic / too deep, so the
 * caller can fall back to the MinCode preset.
 */
export function keymapPresetChain(id: string, lookup: (id: string) => KeymapPreset | undefined = getKeymapPreset): KeymapPreset[] | null {
  const chain: KeymapPreset[] = [];
  const seen = new Set<string>();
  let current: string | undefined = id;
  while (current !== undefined) {
    if (seen.has(current) || chain.length > KEYMAP_PRESET_LIMITS.maxExtendsDepth) return null;
    seen.add(current);
    const preset = lookup(current);
    if (!preset) return null;
    chain.unshift(preset);
    current = preset.extends;
  }
  return chain;
}
