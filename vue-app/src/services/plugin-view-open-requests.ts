// Open requests: the host asks a plugin view to show something (a
// uriHandlers deep link or a searchProviders result). The request opens or
// focuses the view tab, then reaches the view's page frame as
// `mincode.host.open-request` when the page registered `onOpenRequest`
// (features.openRequest). PluginViewFrame queues at most
// PLUGIN_UI_MAX_PENDING_OPEN_REQUESTS until its session is ready; a page that
// never subscribes gets the plain view and the host shows
// pluginContrib.processLaunch.openRequestUnsupported.
import router from "@/router";
import { openPluginViewTab } from "@/domain/workspace/open-plugin-view";
import {
  PLUGIN_UI_MAX_PENDING_OPEN_REQUESTS,
  parsePluginUIOpenRequest,
  type PluginUIOpenRequest,
} from "@/domain/plugin-ui/protocol";
import { listPluginViews } from "@/services/plugin-service";
import { localizedPluginTitle } from "@/services/plugin-titles";
import { useWorkbenchStore } from "@/stores/workbench";

export type PluginViewOpenRequestInput = Omit<PluginUIOpenRequest, "requestId"> & { requestId?: string };
type Receiver = (request: PluginUIOpenRequest) => void;

/** Requests waiting for their view's frame to mount, dropped after this long. */
const UNCLAIMED_TTL_MS = 30_000;

const receivers = new Map<string, Set<Receiver>>();
const unclaimed = new Map<string, Array<{ request: PluginUIOpenRequest; at: number }>>();
let sequence = 0;

function viewKey(pluginId: string, viewId: string) {
  return `${pluginId}\u0000${viewId}`;
}

function takeUnclaimed(key: string) {
  const now = Date.now();
  const waiting = (unclaimed.get(key) ?? []).filter((item) => now - item.at <= UNCLAIMED_TTL_MS);
  unclaimed.delete(key);
  return waiting.map((item) => item.request);
}

/**
 * Registers the page frame of one plugin view as the receiver of its open
 * requests. Requests that arrived before the frame mounted are delivered
 * immediately. Returns the unregister function.
 */
export function registerPluginViewOpenRequestReceiver(pluginId: string, viewId: string, receiver: Receiver): () => void {
  const key = viewKey(pluginId, viewId);
  let set = receivers.get(key);
  if (!set) {
    set = new Set();
    receivers.set(key, set);
  }
  set.add(receiver);
  for (const request of takeUnclaimed(key)) receiver(request);
  return () => {
    const current = receivers.get(key);
    current?.delete(receiver);
    if (current && current.size === 0) receivers.delete(key);
  };
}

async function viewTitle(pluginId: string, viewId: string) {
  const views = await listPluginViews().catch(() => []);
  const view = views.find((item) => item.pluginId === pluginId && item.id === viewId);
  return view ? localizedPluginTitle(view) : viewId;
}

/**
 * Opens or focuses the plugin view tab and delivers request to its page.
 * Returns false when the request is malformed or over 8 KiB (the view still
 * opens) or when the tab could not be opened.
 */
export async function openPluginViewWithRequest(
  pluginId: string,
  viewId: string,
  input: PluginViewOpenRequestInput,
  title?: string,
): Promise<boolean> {
  const request = parsePluginUIOpenRequest({ ...input, requestId: input.requestId ?? `open-${Date.now()}-${++sequence}` });
  const tab = openPluginViewTab(useWorkbenchStore(), router, pluginId, viewId, title ?? await viewTitle(pluginId, viewId));
  if (!tab || !request) return false;
  const key = viewKey(pluginId, viewId);
  const set = receivers.get(key);
  if (set && set.size > 0) {
    for (const receiver of set) receiver(request);
    return true;
  }
  const waiting = unclaimed.get(key) ?? [];
  waiting.push({ request, at: Date.now() });
  unclaimed.set(key, waiting.slice(-PLUGIN_UI_MAX_PENDING_OPEN_REQUESTS));
  return true;
}
