import { JSONStream } from "@/services/desktop-stream";

export const STREAM_SUBSCRIPTION_TIMEOUT_MS = 5_000;
export const STREAM_RECONNECT_DELAYS_MS = [250, 1_000, 3_000, 10_000] as const;

interface ResilientJSONStreamOptions<T> {
  name: string;
  isSubscribed: (value: unknown) => boolean;
  onMessage: (value: T) => void;
  onSubscribed?: () => void;
  subscriptionTimeoutMs?: number;
  reconnectDelaysMs?: readonly number[];
}

/**
 * Maintains a long-lived JSON Stream subscription. A socket is usable only after
 * its explicit subscription frame; close/error/handshake timeout reconnect with
 * bounded exponential backoff. Disposing prevents all future callbacks.
 */
export function listenResilientJSONStream<T>(options: ResilientJSONStreamOptions<T>) {
  const reconnectDelays = options.reconnectDelaysMs?.length
    ? options.reconnectDelaysMs
    : STREAM_RECONNECT_DELAYS_MS;
  const subscriptionTimeoutMs = options.subscriptionTimeoutMs ?? STREAM_SUBSCRIPTION_TIMEOUT_MS;
  let disposed = false;
  let attempt = 0;
  let generation = 0;
  let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  let socket: ReturnType<typeof JSONStream> | undefined;

  const connect = () => {
    if (disposed) return;
    const currentGeneration = ++generation;
    const currentSocket = JSONStream(options.name);
    socket = currentSocket;
    let subscribed = false;
    let failed = false;
    const subscriptionTimer = setTimeout(() => fail(), subscriptionTimeoutMs);

    const fail = () => {
      if (disposed || failed || currentGeneration !== generation) return;
      failed = true;
      clearTimeout(subscriptionTimer);
      try {
        currentSocket.close();
      } catch {
        // 连接可能已经关闭
      }
      const delay = reconnectDelays[Math.min(attempt, reconnectDelays.length - 1)] ?? 10_000;
      attempt += 1;
      reconnectTimer = setTimeout(connect, delay);
    };

    currentSocket.addEventListener("message", (event: MessageEvent) => {
      if (disposed || failed || currentGeneration !== generation) return;
      if (!subscribed && options.isSubscribed(event.data)) {
        subscribed = true;
        attempt = 0;
        clearTimeout(subscriptionTimer);
        options.onSubscribed?.();
        return;
      }
      if (subscribed) options.onMessage(event.data as T);
    });
    currentSocket.addEventListener("error", fail);
    currentSocket.addEventListener("close", fail);
  };

  connect();
  return () => {
    if (disposed) return;
    disposed = true;
    generation += 1;
    if (reconnectTimer) clearTimeout(reconnectTimer);
    try {
      socket?.close();
    } catch {
      // 连接可能已经关闭
    }
  };
}
