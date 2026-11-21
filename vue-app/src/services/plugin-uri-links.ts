// contributes.uriHandlers deep links (mincode://<pluginId>/<path>?<query>).
// The desktop emits `desktop:navigate` with kind "plugin-uri"; a target that
// arrives before the window listens (any kind: a mincode:// link that launched
// the app) is queued by the Go desktop package and drained here once App.vue
// installed its listeners (desktop_take_pending_navigation). This facade stays small because App.vue
// imports it statically: the resolver, confirmation dialog and link gate live
// in plugin-uri-links-runtime.ts and load on the first plugin link.
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

type Runtime = typeof import("@/services/plugin-uri-links-runtime");

let runtime: Promise<Runtime> | undefined;
const loadRuntime = () => (runtime ??= import("@/services/plugin-uri-links-runtime"));

/** Handles one plugin deep link (the raw URI from the desktop). */
export async function handlePluginUriLink(uri: string): Promise<void> {
  if (!isDesktopRuntime()) return;
  await (await loadRuntime()).handlePluginUriLink(uri);
}

/** A queued desktop navigation target, as `desktop:navigate` delivers it live. */
export interface DesktopTarget {
  kind?: string;
  value?: string;
}

/**
 * Drains the targets the desktop received before the window was ready: plugin
 * links go through the cold-start link flow, every other target to onTarget
 * (App.vue's desktop:navigate handler).
 */
export async function drainPendingDesktopNavigation(onTarget?: (target: DesktopTarget) => void): Promise<void> {
  if (!isDesktopRuntime()) return;
  let targets: unknown;
  try {
    targets = await invokeBackend<unknown>("desktop_take_pending_navigation", {});
  } catch {
    return;
  }
  if (!Array.isArray(targets)) return;
  for (const target of targets) {
    const entry = target as { kind?: unknown; value?: unknown } | null;
    if (!entry || typeof entry.kind !== "string" || entry.kind === "plugin-uri") continue;
    onTarget?.({ kind: entry.kind, value: typeof entry.value === "string" ? entry.value : undefined });
  }
  const urls = targets.flatMap((target: unknown) => {
    const entry = target as { kind?: unknown; value?: unknown } | null;
    return entry?.kind === "plugin-uri" && typeof entry.value === "string" ? [entry.value] : [];
  });
  if (!urls.length) return;
  (await loadRuntime()).handleColdStartPluginLinks(urls);
}
