/**
 * Discovered tests of the workspace's test providers, per provider and
 * project: the Tests panel's tree and the editor gutter's run buttons
 * (tests by file and line) share one discovery. A discovery is a provider's
 * `read`-risk discover action; concurrent requests for the same provider
 * and project share one call. Transient: never persisted; the editor
 * discovers again after a save (debounced by its caller).
 *
 * A discovery that fails or finds no tests (the provider's language service
 * may still be starting or importing the project) is a miss: `retryIn` tells
 * the editor when to try again, after TEST_DISCOVERY_RETRY_BASE_MS doubling
 * up to TEST_DISCOVERY_RETRY_MAX_MS, at most TEST_DISCOVERY_RETRY_LIMIT times
 * in a row. A discovery that finds tests, or a save, starts over.
 */
import { shallowRef } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import { testProviderKey, type PluginTestProvider, type TestDiscoverResponse, type TestItem } from "@/domain/plugins/test-providers";
import { testsByFile, type TestFileEntry } from "@/domain/testing/test-discovery";
import type { ProviderInvokeResult } from "@/services/plugin-provider-invoke";
import { discoverTests } from "@/services/plugin-test-provider-service";

type ProviderRef = Pick<PluginTestProvider, "pluginId" | "id">;

export interface TestDiscoveryEntry {
  tests: TestItem[];
  dropped: number;
  truncated: boolean;
  /** Project-relative path → its tests with lines. */
  byFile: Map<string, TestFileEntry[]>;
  /** Bumped per discovery of this provider and project. */
  revision: number;
  /** When it was discovered (ms) and for which locale. */
  at: number;
  locale: string;
  /** A project file that can hold tests was saved since. */
  stale: boolean;
  /** The project's save generation the discovery started in. */
  generation: number;
}

/** A discovery younger than this, of the same locale and without a save since, is reused when the Tests tab opens. */
export const TEST_DISCOVERY_FRESH_MS = 30_000;

/** Retries of discoveries that failed or found nothing: first wait, longest wait, attempts in a row. */
export const TEST_DISCOVERY_RETRY_BASE_MS = 3_000;
export const TEST_DISCOVERY_RETRY_MAX_MS = 30_000;
export const TEST_DISCOVERY_RETRY_LIMIT = 6;

const NO_TESTS: readonly TestFileEntry[] = Object.freeze([]);

export function testDiscoveryKey(provider: ProviderRef, projectRoot: string): string {
  return `${testProviderKey(provider)}@${projectRoot}`;
}

export const useTestDiscovery = defineStore("test-discovery", () => {
  const entries = shallowRef<Record<string, TestDiscoveryEntry>>({});
  /** In-flight discoveries with the project's save generation they started in. */
  const inflight = new Map<string, { generation: number; request: Promise<ProviderInvokeResult<TestDiscoverResponse>> }>();
  /** Per project root, bumped by markStale (a save): older requests saw pre-save content. */
  const generations = new Map<string, number>();
  /** Discoveries in a row that failed or found no tests, and when the last one ended. */
  const misses = new Map<string, { count: number; at: number }>();
  let revision = 0;

  function entry(provider: ProviderRef, projectRoot: string): TestDiscoveryEntry | undefined {
    return entries.value[testDiscoveryKey(provider, projectRoot)];
  }

  function set(provider: ProviderRef, projectRoot: string, response: TestDiscoverResponse, locale = "", generation = generations.get(projectRoot) ?? 0) {
    const stale = generation !== (generations.get(projectRoot) ?? 0);
    revision += 1;
    entries.value = {
      ...entries.value,
      [testDiscoveryKey(provider, projectRoot)]: {
        tests: response.tests,
        dropped: response.dropped,
        truncated: response.truncated,
        byFile: testsByFile(response.tests),
        revision,
        at: Date.now(),
        locale,
        stale,
        generation,
      },
    };
  }

  /**
   * Discovers the provider's tests in the project and keeps them. A call in
   * flight is shared unless a save happened since it started (markStale):
   * then a new call runs, and the older one's result is kept as stale.
   */
  function discover(provider: ProviderRef, projectRoot: string, locale: string): Promise<ProviderInvokeResult<TestDiscoverResponse>> {
    const key = testDiscoveryKey(provider, projectRoot);
    const generation = generations.get(projectRoot) ?? 0;
    const running = inflight.get(key);
    if (running && running.generation === generation) return running.request;
    const request = discoverTests(provider, { projectRoot, locale })
      .then((result) => {
        // A result from before a later save never replaces a newer one (it is kept, as stale, only when nothing newer is).
        const kept = entry(provider, projectRoot);
        if (result.ok && (!kept || kept.generation <= generation)) set(provider, projectRoot, result.response, locale, generation);
        if (result.ok && result.response.tests.length > 0) misses.delete(key);
        else if (result.ok || result.error.code !== "cancelled") misses.set(key, { count: (misses.get(key)?.count ?? 0) + 1, at: Date.now() });
        return result;
      })
      .finally(() => {
        if (inflight.get(key)?.request === request) inflight.delete(key);
      });
    inflight.set(key, { generation, request });
    return request;
  }

  /** The project's discoveries are out of date (a file that can hold tests was saved). */
  function markStale(projectRoot: string) {
    generations.set(projectRoot, (generations.get(projectRoot) ?? 0) + 1);
    for (const key of [...misses.keys()]) if (key.endsWith(`@${projectRoot}`)) misses.delete(key);
    let changed = false;
    const next = { ...entries.value };
    for (const [key, value] of Object.entries(next)) {
      if (key.endsWith(`@${projectRoot}`) && !value.stale) {
        next[key] = { ...value, stale: true };
        changed = true;
      }
    }
    if (changed) entries.value = next;
  }

  /** Whether the kept discovery can be shown without discovering again (see TEST_DISCOVERY_FRESH_MS). */
  function isFresh(provider: ProviderRef, projectRoot: string, locale: string, now = Date.now()): boolean {
    const current = entry(provider, projectRoot);
    return Boolean(current && !current.stale && current.locale === locale && now - current.at < TEST_DISCOVERY_FRESH_MS);
  }

  /**
   * After a miss (the last discoveries failed or found no tests): ms until the
   * next attempt is due (0: now), or Infinity once TEST_DISCOVERY_RETRY_LIMIT
   * retries missed too. Undefined when there is no miss to retry.
   */
  function retryIn(provider: ProviderRef, projectRoot: string, now = Date.now()): number | undefined {
    const miss = misses.get(testDiscoveryKey(provider, projectRoot));
    if (!miss) return undefined;
    if (miss.count > TEST_DISCOVERY_RETRY_LIMIT) return Number.POSITIVE_INFINITY;
    const wait = Math.min(TEST_DISCOVERY_RETRY_BASE_MS * 2 ** (miss.count - 1), TEST_DISCOVERY_RETRY_MAX_MS);
    return Math.max(0, miss.at + wait - now);
  }

  function isDiscovering(provider: ProviderRef, projectRoot: string): boolean {
    return inflight.has(testDiscoveryKey(provider, projectRoot));
  }

  /** The provider's discovered tests of one project file (project-relative path), in tree order. */
  function testsInFile(provider: ProviderRef, projectRoot: string, relativePath: string): readonly TestFileEntry[] {
    return entry(provider, projectRoot)?.byFile.get(relativePath) ?? NO_TESTS;
  }

  return { entries, entry, set, discover, markStale, isFresh, retryIn, isDiscovering, testsInFile };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useTestDiscovery, import.meta.hot));
}
