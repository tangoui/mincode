// Runtime of contributes.uriHandlers deep links, loaded on the first
// plugin link only (see plugin-uri-links.ts, which stays in the entry chunk).
// Every link is resolved by the host (plugin_host_uri_resolve, which never
// starts the plugin), shown in a host confirmation that names the plugin
// (Cancel has the initial focus, in every approval mode), resolved again
// right before acting, and only then opens the plugin's view or runs its
// read-risk command. Identical links within 5 s are ignored and at most 3
// links wait behind the dialog on screen.
import { h } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import PluginLinkConfirmContent from "@/components/plugins/PluginLinkConfirmContent.vue";
import {
  createPluginLinkGate,
  isPluginLinkCandidate,
  parseResolvedPluginLink,
  sameResolvedPluginLink,
  type ResolvedPluginLink,
} from "@/domain/plugins/uri-handlers";
import { useVueI18n, type MessageKey } from "@/i18n/messages";
import { executePluginCommandWithArgs } from "@/services/plugin-commands";
import { parseContributionError, type ContributionError } from "@/services/plugin-contribution-requests";
import { openPluginViewWithRequest } from "@/services/plugin-view-open-requests";
import { invokeBackend, isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";

type ResolveOutcome = { ok: true; link: ResolvedPluginLink } | { ok: false; error: ContributionError };

const gate = createPluginLinkGate();
let showing = false;
/** Cold-start links wait for plugin startup before a not-found answer is final. */
const coldStart = new Set<string>();
const COLD_START_RETRIES = 3;
const COLD_START_WAIT_MS = 2000;

// eslint-disable-next-line no-control-regex -- removing control characters is the point
const UNSAFE_TEXT = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\ufeff]/g;

function plainDetail(detail: string) {
  return detail.replace(UNSAFE_TEXT, " ").slice(0, 200);
}

async function resolveLink(url: string): Promise<ResolveOutcome> {
  try {
    const link = parseResolvedPluginLink(await invokeBackend<unknown>("plugin_host_uri_resolve", { url }));
    return link ? { ok: true, link } : { ok: false, error: { code: "invalid_result", detail: "malformed resolve response" } };
  } catch (error) {
    return { ok: false, error: parseContributionError(error) };
  }
}

function waitForPluginStartup(ms: number) {
  return new Promise<void>((resolve) => {
    let stop: () => void = () => undefined;
    const timer = setTimeout(() => {
      stop();
      resolve();
    }, ms);
    stop = listenAppEvent("plugin:startup-completed", () => {
      clearTimeout(timer);
      stop();
      resolve();
    });
  });
}

async function resolveWithStartupRetry(url: string): Promise<ResolveOutcome> {
  let outcome = await resolveLink(url);
  if (!coldStart.has(url)) return outcome;
  for (let attempt = 0; attempt < COLD_START_RETRIES && !outcome.ok && (outcome.error.code === "not_found" || outcome.error.code === "unavailable"); attempt++) {
    await waitForPluginStartup(COLD_START_WAIT_MS);
    outcome = await resolveLink(url);
  }
  return outcome;
}

function reportResolveError(error: ContributionError) {
  const { t } = useVueI18n();
  const keys: Partial<Record<ContributionError["code"], MessageKey>> = {
    invalid_request: "pluginContrib.uriHandlers.invalid",
    not_found: "pluginContrib.uriHandlers.notFound",
    unavailable: "pluginContrib.uriHandlers.unavailable",
  };
  ElMessage({
    type: "warning",
    message: t(keys[error.code] ?? "pluginContrib.uriHandlers.failed", { detail: plainDetail(error.detail) }),
    grouping: true,
  });
}

async function confirmLink(url: string, link: ResolvedPluginLink): Promise<boolean> {
  const { t } = useVueI18n();
  try {
    await ElMessageBox.confirm(
      h(PluginLinkConfirmContent, { url, link }),
      t("pluginContrib.uriHandlers.confirmTitle"),
      {
        type: "warning",
        confirmButtonText: t("pluginContrib.uriHandlers.confirmOpen"),
        cancelButtonText: t("common.cancel"),
        distinguishCancelAndClose: true,
        autofocus: false,
        closeOnClickModal: false,
        customClass: "app-plugin-link-confirm",
      },
    );
    return true;
  } catch {
    return false;
  }
}

async function act(link: ResolvedPluginLink) {
  if (link.target.type === "view") {
    await openPluginViewWithRequest(
      link.pluginId,
      link.target.view,
      { source: "uri", entry: link.handlerId, path: link.path, pathParams: link.pathParams, params: link.params },
      link.target.viewTitle,
    );
    return;
  }
  await executePluginCommandWithArgs(link.pluginId, link.target.command, { payload: link.payload ?? "", args: link.params });
}

async function processLink(url: string) {
  const first = await resolveWithStartupRetry(url);
  coldStart.delete(url);
  if (!first.ok) {
    reportResolveError(first.error);
    return;
  }
  if (!(await confirmLink(url, first.link))) return;
  // The plugin may have been updated, disabled or reloaded while the dialog was open.
  const second = await resolveLink(url);
  const { t } = useVueI18n();
  if (!second.ok || !sameResolvedPluginLink(first.link, second.link)) {
    ElMessage({ type: "warning", message: t("pluginContrib.uriHandlers.changed"), grouping: true });
    return;
  }
  try {
    await act(second.link);
  } catch (error) {
    ElMessage({ type: "error", message: t("pluginContrib.uriHandlers.failed", { detail: plainDetail(parseContributionError(error).detail) }), grouping: true });
  }
}

async function pump() {
  if (showing) return;
  showing = true;
  try {
    for (let url = gate.take(); url !== undefined; url = gate.take()) {
      await processLink(url);
    }
  } finally {
    showing = false;
  }
}

/** Handles one plugin deep link (the raw URI from the desktop). */
export async function handlePluginUriLink(uri: string): Promise<void> {
  if (!isDesktopRuntime()) return;
  if (!isPluginLinkCandidate(uri)) {
    reportResolveError({ code: "invalid_request", detail: "not a mincode://<plugin>/<path> link" });
    return;
  }
  const offer = gate.offer(uri);
  if (offer === "duplicate") return;
  if (offer === "overflow") {
    ElMessage({ type: "warning", message: useVueI18n().t("pluginContrib.uriHandlers.dropped"), grouping: true });
    return;
  }
  await pump();
}

/** Handles the deep links the desktop queued before the window was ready. */
export function handleColdStartPluginLinks(urls: readonly string[]): void {
  for (const url of urls) {
    coldStart.add(url);
    void handlePluginUriLink(url);
  }
}
