/** Duration texts of the Tests tab (milliseconds under a second, else seconds). */
type Translate = (key: "pluginContrib.testProviders.durationMs" | "pluginContrib.testProviders.durationS", params: Record<string, string | number>) => string;

export function formatTestDuration(t: Translate, ms: number): string {
  if (ms < 1000) return t("pluginContrib.testProviders.durationMs", { ms: Math.round(ms) });
  const seconds = ms / 1000;
  return t("pluginContrib.testProviders.durationS", { s: seconds < 10 ? seconds.toFixed(2) : seconds.toFixed(1) });
}
