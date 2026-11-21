export const AI_REASONING_EFFORTS = [
  "none",
  "minimal",
  "low",
  "medium",
  "high",
  "xhigh",
  "max",
  "ultra",
] as const;

export type AiReasoningEffort = (typeof AI_REASONING_EFFORTS)[number];

/**
 * 用户可以在设置和助手输入框里直接挑选的档位，由低到高排列。
 * `none` / `minimal` 只用于透传已保存的配置，不进入选择列表。
 */
export const AI_REASONING_EFFORT_CHOICES = [
  "low",
  "medium",
  "high",
  "xhigh",
  "max",
  "ultra",
] as const;

export type AiReasoningEffortChoice = (typeof AI_REASONING_EFFORT_CHOICES)[number];

/** 判断某个值是否是可选档位，供 UI 决定要不要回落到「默认」。 */
export function isAiReasoningEffortChoice(value: unknown): value is AiReasoningEffortChoice {
  return AI_REASONING_EFFORT_CHOICES.some((effort) => effort === value);
}

/** Removes UI sentinels and unsupported provider values before native requests. */
export function normalizeAiReasoningEffort(value: unknown): AiReasoningEffort | undefined {
  if (typeof value !== "string") return undefined;
  const normalized = value.trim().toLowerCase();
  return AI_REASONING_EFFORTS.find((effort) => effort === normalized);
}

/** Resolves the assistant's session override without mutating the profile default. */
export function resolveAiReasoningEffort(sessionOverride: unknown, profileDefault: unknown) {
  return normalizeAiReasoningEffort(sessionOverride) ?? normalizeAiReasoningEffort(profileDefault);
}

export interface AiModelAttemptOrderInput {
  /** The model selected for this assistant session. */
  selectedModel?: string;
  /** The active profile's default model. */
  profileDefaultModel: string;
  /** All models configured on the active profile, including its default. */
  configuredModels?: string[];
  /** Whether model failover is enabled for the active profile. */
  fallbackEnabled?: boolean;
  /** Ordered fallback models configured for the active profile. */
  fallbackModels?: string[];
}

export interface SubagentModelAttemptOrderInput {
  /** Model selected for the parent assistant request. */
  inheritedModel: string;
  /** Optional profile-level override for subagent requests. */
  subagentModel?: string;
  configuredModels?: string[];
  fallbackEnabled?: boolean;
  fallbackModels?: string[];
}

export interface AiModelProfile {
  id?: string;
  model?: string;
  commonModels?: unknown;
}

/** Mutable fields that describe a profile's selectable and retryable models. */
export interface AiProfileModelConfiguration extends AiModelProfile {
  failoverModels?: unknown;
}

export interface AiProfileModelConfigurationAfterRemoval {
  model: string;
  commonModels: string[];
  failoverModels: string[];
}

/**
 * Removes a model without leaving it as the profile default or a failover target.
 * When the removed model is the default, the first remaining configured model is
 * promoted. An empty result means the caller must require a replacement first.
 */
export function removeAiProfileModel(
  profile: AiProfileModelConfiguration,
  removedModel: unknown,
): AiProfileModelConfigurationAfterRemoval | undefined {
  const removed = typeof removedModel === "string" ? removedModel.trim() : "";
  const current = typeof profile.model === "string" ? profile.model.trim() : "";
  const common = Array.isArray(profile.commonModels)
    ? profile.commonModels.filter((item): item is string => typeof item === "string")
    : [];
  const failover = Array.isArray(profile.failoverModels)
    ? profile.failoverModels.filter((item): item is string => typeof item === "string")
    : [];
  const remaining = configuredAiModels(current, common).filter((model) => model !== removed);
  const model = current === removed ? remaining[0] ?? "" : current;
  if (!model) return undefined;
  return {
    model,
    commonModels: common.filter((item) => item.trim() && item.trim() !== removed),
    failoverModels: failover.filter((item) => item.trim() && item.trim() !== removed && item.trim() !== model),
  };
}

/** OpenAI models before GPT-5 (gpt-3.5, gpt-4*, o1/o3/o4), also behind a router prefix such as "openai/". */
const RETIRED_AI_MODEL_PATTERN = /^(?:gpt-3\.5|gpt-4(?:o|\.\d+)?|chatgpt-4o|o[134])(?:[-.:]|$)/;

export function isRetiredAiModel(model: unknown) {
  if (typeof model !== "string") return false;
  const name = model.trim().toLowerCase();
  return RETIRED_AI_MODEL_PATTERN.test(name.slice(name.lastIndexOf("/") + 1));
}

export interface AiProfileModelLists {
  model: string;
  commonModels: string[];
  failoverModels: string[];
  subagentModel: string;
}

/**
 * Drops retired models from a saved profile. The default moves to the first
 * remaining model; a profile left with none gets the provider preset's models.
 * Returns the same object when nothing is retired.
 */
export function withoutRetiredAiModels<T extends AiProfileModelLists>(
  profile: T,
  preset: { model: string; commonModels: readonly string[] },
): T {
  const lists = [profile.model, profile.subagentModel, ...profile.commonModels, ...profile.failoverModels];
  if (!lists.some(isRetiredAiModel)) return profile;
  let commonModels = profile.commonModels.filter((model) => !isRetiredAiModel(model));
  let model = isRetiredAiModel(profile.model) ? "" : profile.model;
  if (!model && !commonModels.length) {
    model = preset.model;
    commonModels = [...preset.commonModels];
  }
  model ||= commonModels[0]!;
  return {
    ...profile,
    model,
    commonModels,
    failoverModels: profile.failoverModels.filter((item) => !isRetiredAiModel(item) && item !== model),
    subagentModel: isRetiredAiModel(profile.subagentModel) ? "" : profile.subagentModel,
  };
}

export interface AiProfileModelSelection {
  profileId: string;
  model: string;
}

export interface ResolveAiModelSelectionInput<T extends AiModelProfile> {
  profiles?: readonly T[];
  activeProfileId?: unknown;
  /** The active assistant conversation's encoded `profileId::model` value. */
  selectedModel?: unknown;
  /** Persisted cross-feature default for AI actions without a model picker. */
  defaultSelection?: unknown;
}

function normalize(models: Array<string | undefined | null>) {
  return [...new Set(
    models
      .filter((model): model is string => typeof model === "string")
      .map((model) => model.trim())
      .filter(Boolean),
  )];
}

/** Returns the unique, non-empty models that belong to an AI profile. */
export function configuredAiModels(configuredModel: string, commonModels: string[] = []) {
  return normalize([configuredModel, ...commonModels]);
}

/** Encodes a picker option without conflating models belonging to different profiles. */
export function aiModelOptionValue(profileId: string, model: string) {
  return `${profileId}::${model}`;
}

export function normalizeAiProfileModelSelection(value: unknown): AiProfileModelSelection | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
    const selection = value as Record<string, unknown>;
  const profileId = typeof selection.profileId === "string" ? selection.profileId.trim() : "";
    const model = typeof selection.model === "string" ? selection.model.trim() : "";
  return profileId && model ? { profileId, model } : undefined;
}

function selectionFromOption<T extends AiModelProfile>(value: unknown, profiles: readonly T[]) {
  if (typeof value !== "string") return undefined;
  const normalized = value.trim();
  const profile = profiles
    .filter((item) => typeof item.id === "string" && item.id.trim())
    .sort((left, right) => (right.id?.length ?? 0) - (left.id?.length ?? 0))
    .find((item) => normalized.startsWith(`${item.id!.trim()}::`));
  if (!profile?.id) return undefined;
  const model = normalized.slice(profile.id.trim().length + 2).trim();
  return model ? { profileId: profile.id.trim(), model } : undefined;
}

function profileSupportsModel(profile: AiModelProfile, model: string) {
  const commonModels = Array.isArray(profile.commonModels)
    ? profile.commonModels.filter((item): item is string => typeof item === "string")
    : [];
  return configuredAiModels(profile.model ?? "", commonModels).includes(model);
}

function validSelection<T extends AiModelProfile>(
  selection: AiProfileModelSelection | undefined,
  profiles: readonly T[],
) {
  if (!selection) return undefined;
  const profile = profiles.find((item) => item.id?.trim() === selection.profileId);
  return profile && profileSupportsModel(profile, selection.model)
    ? { profile, selection }
    : undefined;
}

/**
 * Resolves the one model used by AI actions without their own picker.
 * An active assistant conversation wins immediately; its persisted global
 * default survives page changes; the active Settings profile is the fallback.
 */
export function resolveAiModelSelection<T extends AiModelProfile>(
  input: ResolveAiModelSelectionInput<T>,
) {
  const profiles = input.profiles ?? [];
  const sessionSelection = validSelection(selectionFromOption(input.selectedModel, profiles), profiles);
  if (sessionSelection) return sessionSelection;

  const persistedSelection = validSelection(normalizeAiProfileModelSelection(input.defaultSelection), profiles);
  if (persistedSelection) return persistedSelection;

  const activeProfile = profiles.find((profile) => profile.id?.trim() === input.activeProfileId)
    ?? profiles[0];
  if (!activeProfile) return undefined;
  const [model] = configuredAiModels(
    activeProfile.model ?? "",
    Array.isArray(activeProfile.commonModels)
      ? activeProfile.commonModels.filter((item): item is string => typeof item === "string")
      : [],
  );
  const profileId = activeProfile.id?.trim();
  return profileId && model
    ? { profile: activeProfile, selection: { profileId, model } }
    : undefined;
}

/** Builds the subagent chain, preserving inheritance when no override is configured. */
export function buildSubagentModelAttemptOrder(input: SubagentModelAttemptOrderInput) {
  const configured = normalize(input.configuredModels ?? []);
  const inherited = input.inheritedModel.trim();
  const override = input.subagentModel?.trim();
  const first = override && configured.includes(override) ? override : inherited;
  if (!first || !input.fallbackEnabled) return first ? [first] : [];
  const fallbacks = normalize(input.fallbackModels ?? [])
    .filter((model) => configured.includes(model));
  return normalize([first, ...fallbacks]);
}

/**
 * Builds the per-request model attempt order. The first attempt is always the
 * model explicitly selected in the assistant. Additional attempts are only the
 * profile's explicitly configured fallbacks, never a task/heuristic route.
 */
export function buildAiModelAttemptOrder(input: AiModelAttemptOrderInput) {
  const configured = normalize(input.configuredModels?.length
    ? input.configuredModels
    : [input.profileDefaultModel]);
  const defaultModel = input.profileDefaultModel.trim();
  const selected = input.selectedModel?.trim();
  const first = selected && configured.includes(selected)
    ? selected
    : defaultModel && configured.includes(defaultModel)
      ? defaultModel
      : configured[0] ?? "";

  if (!first || !input.fallbackEnabled) return first ? [first] : [];

  const fallbacks = normalize(input.fallbackModels ?? [])
    .filter((model) => configured.includes(model));
  return normalize([first, ...fallbacks]);
}
