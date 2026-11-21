export type RuntimePerformanceProfile = "low-memory" | "balanced" | "high-performance" | "custom";

export interface RuntimePerformanceSettings {
  performanceProfile: RuntimePerformanceProfile;
  lspBackgroundStopSeconds: number;
  lspIdleStopSeconds: number;
  lspAuxiliaryIdleStopSeconds: number;
  aiInactiveWorkspaceCacheCount: number;
  aiInactiveWorkspaceCacheMiB: number;
  terminalLayoutCacheLimit: number;
}

export const DEFAULT_RUNTIME_PERFORMANCE_SETTINGS: RuntimePerformanceSettings = {
  performanceProfile: "balanced",
  lspBackgroundStopSeconds: 90,
  lspIdleStopSeconds: 180,
  lspAuxiliaryIdleStopSeconds: 60,
  aiInactiveWorkspaceCacheCount: 2,
  aiInactiveWorkspaceCacheMiB: 8,
  terminalLayoutCacheLimit: 50,
};

export const RUNTIME_PERFORMANCE_PRESETS: Record<
  Exclude<RuntimePerformanceProfile, "custom">,
  Omit<RuntimePerformanceSettings, "performanceProfile">
> = {
  "low-memory": {
    lspBackgroundStopSeconds: 30,
    lspIdleStopSeconds: 60,
    lspAuxiliaryIdleStopSeconds: 30,
    aiInactiveWorkspaceCacheCount: 1,
    aiInactiveWorkspaceCacheMiB: 4,
    terminalLayoutCacheLimit: 20,
  },
  balanced: {
    lspBackgroundStopSeconds: 90,
    lspIdleStopSeconds: 180,
    lspAuxiliaryIdleStopSeconds: 60,
    aiInactiveWorkspaceCacheCount: 2,
    aiInactiveWorkspaceCacheMiB: 8,
    terminalLayoutCacheLimit: 50,
  },
  "high-performance": {
    lspBackgroundStopSeconds: 300,
    lspIdleStopSeconds: 600,
    lspAuxiliaryIdleStopSeconds: 180,
    aiInactiveWorkspaceCacheCount: 4,
    aiInactiveWorkspaceCacheMiB: 16,
    terminalLayoutCacheLimit: 100,
  },
};

// 0 表示不限制：不自动回收 / 不做缓存淘汰，其余值受安全上限约束。
const RUNTIME_LIMITS = {
  lspBackgroundStopSeconds: { min: 0, max: 600 },
  lspIdleStopSeconds: { min: 0, max: 1800 },
  lspAuxiliaryIdleStopSeconds: { min: 0, max: 600 },
  aiInactiveWorkspaceCacheCount: { min: 0, max: 5 },
  aiInactiveWorkspaceCacheMiB: { min: 0, max: 32 },
  terminalLayoutCacheLimit: { min: 0, max: 100 },
} as const;

function boundedInteger(value: unknown, fallback: number, min: number, max: number) {
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) return fallback;
  return Math.min(max, Math.max(min, Math.round(parsed)));
}

function isProfile(value: unknown): value is RuntimePerformanceProfile {
  return value === "low-memory" || value === "balanced" || value === "high-performance" || value === "custom";
}

export function normalizeRuntimePerformanceSettings(value: unknown): RuntimePerformanceSettings {
  const raw = value && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : {};
  const profile = isProfile(raw.performanceProfile) ? raw.performanceProfile : DEFAULT_RUNTIME_PERFORMANCE_SETTINGS.performanceProfile;
  const fallback = DEFAULT_RUNTIME_PERFORMANCE_SETTINGS;
  return {
    performanceProfile: profile,
    lspBackgroundStopSeconds: boundedInteger(
      raw.lspBackgroundStopSeconds,
      fallback.lspBackgroundStopSeconds,
      RUNTIME_LIMITS.lspBackgroundStopSeconds.min,
      RUNTIME_LIMITS.lspBackgroundStopSeconds.max,
    ),
    lspIdleStopSeconds: boundedInteger(
      raw.lspIdleStopSeconds,
      fallback.lspIdleStopSeconds,
      RUNTIME_LIMITS.lspIdleStopSeconds.min,
      RUNTIME_LIMITS.lspIdleStopSeconds.max,
    ),
    lspAuxiliaryIdleStopSeconds: boundedInteger(
      raw.lspAuxiliaryIdleStopSeconds,
      fallback.lspAuxiliaryIdleStopSeconds,
      RUNTIME_LIMITS.lspAuxiliaryIdleStopSeconds.min,
      RUNTIME_LIMITS.lspAuxiliaryIdleStopSeconds.max,
    ),
    aiInactiveWorkspaceCacheCount: boundedInteger(
      raw.aiInactiveWorkspaceCacheCount,
      fallback.aiInactiveWorkspaceCacheCount,
      RUNTIME_LIMITS.aiInactiveWorkspaceCacheCount.min,
      RUNTIME_LIMITS.aiInactiveWorkspaceCacheCount.max,
    ),
    aiInactiveWorkspaceCacheMiB: boundedInteger(
      raw.aiInactiveWorkspaceCacheMiB,
      fallback.aiInactiveWorkspaceCacheMiB,
      RUNTIME_LIMITS.aiInactiveWorkspaceCacheMiB.min,
      RUNTIME_LIMITS.aiInactiveWorkspaceCacheMiB.max,
    ),
    terminalLayoutCacheLimit: boundedInteger(
      raw.terminalLayoutCacheLimit,
      fallback.terminalLayoutCacheLimit,
      RUNTIME_LIMITS.terminalLayoutCacheLimit.min,
      RUNTIME_LIMITS.terminalLayoutCacheLimit.max,
    ),
  };
}

export function resolveRuntimePerformanceSettings(value: unknown): RuntimePerformanceSettings {
  const normalized = normalizeRuntimePerformanceSettings(value);
  if (normalized.performanceProfile === "custom") return normalized;
  return {
    performanceProfile: normalized.performanceProfile,
    ...RUNTIME_PERFORMANCE_PRESETS[normalized.performanceProfile],
  };
}

export function runtimePerformanceSettingsFromSnapshot(value: unknown): RuntimePerformanceSettings {
  return resolveRuntimePerformanceSettings(value);
}


export const RUNTIME_PERFORMANCE_SETTING_KEYS = [
  "performanceProfile",
  "lspBackgroundStopSeconds",
  "lspIdleStopSeconds",
  "lspAuxiliaryIdleStopSeconds",
  "aiInactiveWorkspaceCacheCount",
  "aiInactiveWorkspaceCacheMiB",
  "terminalLayoutCacheLimit",
] as const satisfies readonly (keyof RuntimePerformanceSettings)[];

export const RUNTIME_PERFORMANCE_LIMITS = RUNTIME_LIMITS;
