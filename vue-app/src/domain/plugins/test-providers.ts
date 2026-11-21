/**
 * Frontend stage of the `testProviders` contribution point: re-validation of
 * the providers the Go host accepted
 * (backend/plugin/contrib_test_providers.go) and of the results of
 * `plugin_host_test_discover` / `plugin_host_test_run`
 * (test_provider_invoke.go). Discovery returns a plain-text test tree with
 * optional project-relative locations; a run returns a status per test id
 * plus sanitized output. Tests run only on an explicit user action. No DOM,
 * Vue or IPC here; the runtime wiring lives in
 * services/contributions/test-providers.ts, stores/plugin-test-providers.ts,
 * services/plugin-test-provider-service.ts and
 * components/projects/ProjectPluginTestsPanel.vue.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import {
  PROVIDER_ACTION_ID_PATTERN,
  PROVIDER_ENTRY_ID_PATTERN,
  boundedLine,
  collectProviderEntries,
  isProjectRelativePath,
  isProviderItemId,
  isRecord,
  localizedTitles,
  providerPreviewTitles,
  responseCount,
  type ProviderPointIssue,
} from "@/domain/plugins/provider-points";
import { resolvePluginTitle } from "@/domain/plugins/title";

export const TEST_PROVIDERS_CONTRIBUTION_POINT = "testProviders";

/** Mirrors contribution-points.json testProviders limits (the host enforces them first). */
export const TEST_PROVIDER_LIMITS = {
  maxEntries: 4,
  titleMaxRunes: 48,
  localesMax: 16,
  testsMax: 2000,
  testIdMaxLength: 256,
  labelMaxRunes: 256,
  depthMax: 8,
  pathMaxLength: 1024,
  lineMax: 10_000_000,
  runTestsMax: 2000,
  messageMaxRunes: 4096,
  outputMaxRunes: 65536,
  durationMsMax: 86_400_000,
  discoverTimeoutMs: 15000,
  runTimeoutMs: 30000,
  /** Streaming test runs (debugRun, host 0.3.0); see domain/testing/test-runs.ts. */
  traceMaxBytes: 65536,
  outputTextMaxBytes: 65536,
  runNodesMax: 10000,
} as const;

/** The debugger type pattern of the debuggers point (Go debuggerTypePattern). */
const DEBUGGER_TYPE_PATTERN = /^[a-z][a-z0-9-]{0,31}$/;

export const TEST_STATUSES = ["passed", "failed", "skipped", "errored"] as const;
export type TestStatus = (typeof TEST_STATUSES)[number];
const STATUSES: ReadonlySet<string> = new Set(TEST_STATUSES);

export type TestRunRisk = "read" | "write" | "dangerous";
const RISKS: ReadonlySet<string> = new Set(["read", "write", "dangerous"]);

export interface PluginTestProvider {
  pluginId: string;
  pluginName: string;
  id: string;
  title: string;
  titleLocalized?: Record<string, string>;
  /** The run action's risk; anything but "read" asks the user before the first run. */
  runRisk: TestRunRisk;
  /**
   * Set when the provider runs and debugs tests as debug sessions of its
   * plugin's debugger with streamed results (host 0.3.0; services/plugin-test-runs.ts).
   */
  debugRun?: { debuggerType: string };
}

export type TestProviderIssue = ProviderPointIssue;

export interface TestItem {
  id: string;
  label: string;
  path?: string;
  line?: number;
  children?: TestItem[];
}

export interface TestDiscoverResponse {
  tests: TestItem[];
  dropped: number;
  truncated: boolean;
}

export interface TestResult {
  id: string;
  status: TestStatus;
  message?: string;
  durationMs?: number;
}

export interface TestRunResponse {
  results: TestResult[];
  output?: string;
  outputTruncated: boolean;
  dropped: number;
  truncated: boolean;
}

/** Re-validates one host-accepted entry (defense in depth). */
export function validateTestProviderEntry(
  raw: unknown,
): { ok: true; value: Omit<PluginTestProvider, "pluginId" | "pluginName"> } | { ok: false; error: string } {
  if (!isRecord(raw)) return { ok: false, error: "entry must be an object" };
  const id = typeof raw.id === "string" ? raw.id : "";
  if (!PROVIDER_ENTRY_ID_PATTERN.test(id)) return { ok: false, error: `test provider id "${id}" is invalid` };
  const title = boundedLine(raw.title, TEST_PROVIDER_LIMITS.titleMaxRunes, true);
  if (!title) return { ok: false, error: `test provider "${id}" title is invalid` };
  const titleLocalized = localizedTitles(raw.titleLocalized, TEST_PROVIDER_LIMITS.titleMaxRunes, TEST_PROVIDER_LIMITS.localesMax);
  if (titleLocalized === false) return { ok: false, error: `test provider "${id}" titleLocalized is invalid` };
  for (const key of ["discoverAction", "runAction"] as const) {
    if (typeof raw[key] !== "string" || !PROVIDER_ACTION_ID_PATTERN.test(raw[key])) return { ok: false, error: `test provider "${id}" ${key} is invalid` };
  }
  if (raw.discoverAction === raw.runAction) return { ok: false, error: `test provider "${id}" runAction must differ from discoverAction` };
  const risk = raw.runRisk === undefined ? "read" : raw.runRisk;
  // An unknown risk is treated as the strictest one.
  const runRisk: TestRunRisk = typeof risk === "string" && RISKS.has(risk) ? (risk as TestRunRisk) : "dangerous";
  let debugRun: PluginTestProvider["debugRun"];
  if (raw.debugRun !== undefined) {
    const value = raw.debugRun;
    if (!isRecord(value) || typeof value.debuggerType !== "string" || !DEBUGGER_TYPE_PATTERN.test(value.debuggerType) ||
      typeof value.resolveRunAction !== "string" || !PROVIDER_ACTION_ID_PATTERN.test(value.resolveRunAction)) {
      return { ok: false, error: `test provider "${id}" debugRun is invalid` };
    }
    debugRun = { debuggerType: value.debuggerType };
  }
  return { ok: true, value: { id, title, ...(titleLocalized ? { titleLocalized } : {}), runRisk, ...(debugRun ? { debugRun } : {}) } };
}

/** The providers of the active process plugins, in ascending plugin id order. */
export function collectTestProviders(contributions: readonly PointContribution[]): { providers: PluginTestProvider[]; issues: TestProviderIssue[] } {
  const { entries, issues } = collectProviderEntries(contributions, TEST_PROVIDER_LIMITS.maxEntries, validateTestProviderEntry, "test provider");
  return { providers: entries, issues };
}

export function testProviderTitle(provider: PluginTestProvider, locale: string | readonly string[]): string {
  return resolvePluginTitle(provider, locale);
}

export function testProviderKey(provider: Pick<PluginTestProvider, "pluginId" | "id">): string {
  return `${provider.pluginId}/${provider.id}`;
}

// eslint-disable-next-line no-control-regex -- stripping control characters is the point
const UNSAFE_TEXT = /[\u0000-\u0008\u000b-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\u2060\ufeff]/g;

/** Plain multi-line text with unsafe characters removed, cut to maxRunes. */
export function sanitizeTestText(value: string, maxRunes: number): { text: string; truncated: boolean } {
  const clean = value.replace(/\r\n?/g, "\n").replace(UNSAFE_TEXT, "");
  const runes = [...clean];
  if (runes.length <= maxRunes) return { text: clean, truncated: false };
  return { text: runes.slice(0, maxRunes).join(""), truncated: true };
}

/**
 * Re-validates a `plugin_host_test_discover` response. An invalid or
 * duplicate node (ids are unique across the tree) is dropped with its
 * children and counted; nodes deeper than depthMax are dropped; the walk
 * stops after testsMax nodes.
 */
export function parseTestDiscoverResponse(value: unknown): TestDiscoverResponse {
  const response: TestDiscoverResponse = { tests: [], dropped: 0, truncated: false };
  if (!isRecord(value) || !Array.isArray(value.tests)) return response;
  response.dropped = responseCount(value.dropped);
  response.truncated = value.truncated === true;
  const seen = new Set<string>();
  const walk = (items: unknown[], depth: number): TestItem[] => {
    const out: TestItem[] = [];
    for (const raw of items) {
      if (seen.size >= TEST_PROVIDER_LIMITS.testsMax) {
        response.truncated = true;
        return out;
      }
      if (depth > TEST_PROVIDER_LIMITS.depthMax || !isRecord(raw)) {
        response.dropped += 1;
        continue;
      }
      const label = boundedLine(raw.label, TEST_PROVIDER_LIMITS.labelMaxRunes, true);
      const path = raw.path === undefined ? undefined : isProjectRelativePath(raw.path, TEST_PROVIDER_LIMITS.pathMaxLength) ? raw.path : false;
      const line = raw.line;
      const badLine = line !== undefined && (typeof line !== "number" || !Number.isInteger(line) || line < 1 || line > TEST_PROVIDER_LIMITS.lineMax || !path);
      const children = raw.children;
      if (!isProviderItemId(raw.id, TEST_PROVIDER_LIMITS.testIdMaxLength) || seen.has(raw.id) || !label || path === false || badLine ||
        (children !== undefined && !Array.isArray(children))) {
        response.dropped += 1;
        continue;
      }
      seen.add(raw.id);
      const item: TestItem = { id: raw.id, label, ...(path ? { path } : {}), ...(typeof line === "number" ? { line } : {}) };
      if (Array.isArray(children) && children.length) {
        const nested = walk(children, depth + 1);
        if (nested.length) item.children = nested;
      }
      out.push(item);
    }
    return out;
  };
  response.tests = walk(value.tests, 1);
  return response;
}

/** Every test id of a tree, depth-first. */
export function testIds(items: readonly TestItem[]): string[] {
  const ids: string[] = [];
  const visit = (list: readonly TestItem[]) => {
    for (const item of list) {
      ids.push(item.id);
      if (item.children) visit(item.children);
    }
  };
  visit(items);
  return ids;
}

/**
 * Re-validates a `plugin_host_test_run` response. Results whose id is not in
 * `known` (the discovered tree) are dropped and counted, as are invalid or
 * duplicate ones.
 */
export function parseTestRunResponse(value: unknown, known: ReadonlySet<string>): TestRunResponse {
  const response: TestRunResponse = { results: [], outputTruncated: false, dropped: 0, truncated: false };
  if (!isRecord(value) || !Array.isArray(value.results)) return response;
  response.dropped = responseCount(value.dropped);
  response.truncated = value.truncated === true || value.results.length > TEST_PROVIDER_LIMITS.testsMax;
  const seen = new Set<string>();
  for (const raw of value.results.slice(0, TEST_PROVIDER_LIMITS.testsMax)) {
    const item = isRecord(raw) ? raw : {};
    const id = isProviderItemId(item.id, TEST_PROVIDER_LIMITS.testIdMaxLength) ? item.id : "";
    const status = typeof item.status === "string" && STATUSES.has(item.status) ? (item.status as TestStatus) : undefined;
    const duration = item.durationMs;
    const badDuration = duration !== undefined &&
      (typeof duration !== "number" || !Number.isFinite(duration) || duration < 0 || duration > TEST_PROVIDER_LIMITS.durationMsMax);
    const badMessage = item.message !== undefined && typeof item.message !== "string";
    if (!id || !known.has(id) || seen.has(id) || !status || badDuration || badMessage) {
      response.dropped += 1;
      continue;
    }
    seen.add(id);
    const message = typeof item.message === "string" ? sanitizeTestText(item.message, TEST_PROVIDER_LIMITS.messageMaxRunes).text : "";
    response.results.push({
      id,
      status,
      ...(message.trim() ? { message } : {}),
      ...(typeof duration === "number" ? { durationMs: Math.round(duration) } : {}),
    });
  }
  if (typeof value.output === "string" && value.output) {
    const output = sanitizeTestText(value.output, TEST_PROVIDER_LIMITS.outputMaxRunes);
    response.output = output.text;
    response.outputTruncated = output.truncated || value.outputTruncated === true;
  }
  return response;
}

/** Aggregated status of a node: its own result, else the worst of its children. */
export function aggregateTestStatus(item: TestItem, results: ReadonlyMap<string, TestResult>): TestStatus | undefined {
  const own = results.get(item.id)?.status;
  if (own) return own;
  if (!item.children) return undefined;
  const order: TestStatus[] = ["errored", "failed", "passed", "skipped"];
  const statuses = new Set(item.children.map((child) => aggregateTestStatus(child, results)).filter((status): status is TestStatus => Boolean(status)));
  return order.find((status) => statuses.has(status));
}

/** Install-preview summary: the titles of the test providers a manifest would add. */
export function testProviderPreviewSummary(contributes: unknown): string[] {
  return providerPreviewTitles(contributes, TEST_PROVIDERS_CONTRIBUTION_POINT, TEST_PROVIDER_LIMITS.maxEntries, TEST_PROVIDER_LIMITS.titleMaxRunes);
}
