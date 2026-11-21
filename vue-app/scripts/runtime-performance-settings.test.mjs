import assert from "node:assert/strict";
import test from "node:test";
import {
  DEFAULT_RUNTIME_PERFORMANCE_SETTINGS,
  RUNTIME_PERFORMANCE_PRESETS,
  normalizeRuntimePerformanceSettings,
  resolveRuntimePerformanceSettings,
} from "../src/domain/performance/runtime-limits.ts";

test("性能配置缺失时使用平衡预设", () => {
  assert.deepEqual(resolveRuntimePerformanceSettings(undefined), DEFAULT_RUNTIME_PERFORMANCE_SETTINGS);
});

test("预设不接受被持久化值绕过", () => {
  assert.deepEqual(
    resolveRuntimePerformanceSettings({ performanceProfile: "low-memory", lspIdleStopSeconds: 1800 }),
    { performanceProfile: "low-memory", ...RUNTIME_PERFORMANCE_PRESETS["low-memory"] },
  );
});

test("自定义性能配置受硬边界约束", () => {
  assert.deepEqual(
    normalizeRuntimePerformanceSettings({
      performanceProfile: "custom",
      lspBackgroundStopSeconds: 1,
      lspIdleStopSeconds: 99999,
      lspAuxiliaryIdleStopSeconds: 1,
      aiInactiveWorkspaceCacheCount: -5,
      aiInactiveWorkspaceCacheMiB: 999,
      terminalLayoutCacheLimit: 999,
    }),
    {
      performanceProfile: "custom",
      lspBackgroundStopSeconds: 1,
      lspIdleStopSeconds: 1800,
      lspAuxiliaryIdleStopSeconds: 1,
      aiInactiveWorkspaceCacheCount: 0,
      aiInactiveWorkspaceCacheMiB: 32,
      terminalLayoutCacheLimit: 100,
    },
  );
});
