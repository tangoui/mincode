/**
 * Calls a plugin test provider through the host
 * (`plugin_host_test_discover`, `plugin_host_test_run`). The host
 * canonicalizes the project root, builds the fixed payload and bounds time,
 * payload and result; the responses are re-validated here. Runs are only
 * started by an explicit user action in the Tests panel.
 */
import {
  parseTestDiscoverResponse,
  parseTestRunResponse,
  TEST_PROVIDER_LIMITS,
  type PluginTestProvider,
  type TestDiscoverResponse,
  type TestRunResponse,
} from "@/domain/plugins/test-providers";
import { invokeProviderCommand, type ProviderInvokeResult } from "@/services/plugin-provider-invoke";

type ProviderRef = Pick<PluginTestProvider, "pluginId" | "id">;

export function discoverTests(
  provider: ProviderRef,
  request: { projectRoot: string; locale: string },
  signal?: AbortSignal,
): Promise<ProviderInvokeResult<TestDiscoverResponse>> {
  return invokeProviderCommand(
    "plugin_host_test_discover",
    { pluginId: provider.pluginId, providerId: provider.id, projectRoot: request.projectRoot, locale: request.locale },
    parseTestDiscoverResponse,
    signal,
  );
}

/** Runs the given tests (an empty list runs all); results for unknown ids are dropped. */
export function runTests(
  provider: ProviderRef,
  request: { projectRoot: string; locale: string; tests: readonly string[]; known: ReadonlySet<string> },
  signal?: AbortSignal,
): Promise<ProviderInvokeResult<TestRunResponse>> {
  return invokeProviderCommand(
    "plugin_host_test_run",
    {
      pluginId: provider.pluginId,
      providerId: provider.id,
      projectRoot: request.projectRoot,
      tests: request.tests.slice(0, TEST_PROVIDER_LIMITS.runTestsMax),
      locale: request.locale,
    },
    (value) => parseTestRunResponse(value, request.known),
    signal,
  );
}
