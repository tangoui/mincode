import { ref } from "vue";

export interface CloudPayloadEvent {
  relayId: string;
  requestId: string;
  sourceDeviceId: string;
  channel: string;
  sequence: number;
  payload: Record<string, unknown>;
  replayed: boolean;
}

const MAX_PROCESSED_REQUESTS = 2_000;
const pending = ref<CloudPayloadEvent[]>([]);
const processed = new Set<string>();
const processedOrder: string[] = [];
const listeners = new Set<() => void>();

function notify() {
  for (const listener of listeners) listener();
}

export function enqueueCloudPayload(event: CloudPayloadEvent) {
  if (!event.requestId || processed.has(event.requestId)) return false;
  if (pending.value.some((item) => item.requestId === event.requestId)) {
    notify();
    return false;
  }
  pending.value.push(event);
  notify();
  return true;
}

export function listCloudPayloads(channel: string) {
  return pending.value.filter((event) => event.channel === channel);
}

export function completeCloudPayload(requestId: string) {
  const index = pending.value.findIndex((event) => event.requestId === requestId);
  if (index < 0) return false;
  pending.value.splice(index, 1);
  processed.add(requestId);
  processedOrder.push(requestId);
  while (processedOrder.length > MAX_PROCESSED_REQUESTS) {
    const expired = processedOrder.shift();
    if (expired) processed.delete(expired);
  }
  return true;
}

export function subscribeCloudPayloads(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}
