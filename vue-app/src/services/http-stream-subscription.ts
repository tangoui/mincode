import { invokeBackend, listenAppEvent } from "@/services/desktop-bridge";
import { isStreamAvailable, JSONStream } from "@/services/desktop-stream";

export interface HttpStreamSubscription {
  /** 后端确认订阅后 resolve；必须在发起连接命令前等待，避免丢失首批事件。 */
  ready: Promise<void>;
  stop(): void;
}

interface SubscribeOptions<T> {
  onEvent(event: T): void;
  /** 订阅成功后本地数据通道断开时调用；远端会话已被请求断开。 */
  onTransportLost(): void;
}

/** 订阅后端某个 HTTP 流会话（WebSocket / SSE / Socket.IO）的事件。 */
export function subscribeHttpStream<T extends { type?: string }>(
  sessionId: string,
  options: SubscribeOptions<T>
): HttpStreamSubscription {
  if (!isStreamAvailable()) {
    return {
      ready: Promise.resolve(),
      stop: listenAppEvent<T>(`http-stream:${sessionId}`, options.onEvent),
    };
  }
  // 桌面运行时只消费 Streams 数据面。
  const socket = JSONStream("http-stream");
  let stopped = false;
  let subscriptionSettled = false;
  let subscribedSuccessfully = false;
  const closeSocket = () => {
    try {
      socket.close();
    } catch {
      // 连接可能已经关闭
    }
  };
  const handleTransportFailure = (rejectSubscription: (reason: Error) => void) => {
    if (stopped) return;
    const transportError = new Error("HTTP Stream transport disconnected");
    if (!subscriptionSettled) {
      subscriptionSettled = true;
      rejectSubscription(transportError);
      return;
    }
    if (!subscribedSuccessfully) return;
    stopped = true;
    closeSocket();
    void invokeBackend("http_stream_disconnect", { sessionId }).catch(() => undefined);
    options.onTransportLost();
  };
  const ready = new Promise<void>((resolve, reject) => {
    const timer = window.setTimeout(() => {
      if (subscriptionSettled) return;
      subscriptionSettled = true;
      reject(new Error("HTTP Stream subscription timed out"));
    }, 5000);
    const sendHello = () => socket.send({ sessionId });
    if (socket.readyState === 1) sendHello();
    else socket.addEventListener("open", sendHello, { once: true });
    socket.addEventListener("message", (event: MessageEvent) => {
      const data = event.data as T | { type?: "subscribed" } | undefined;
      if (stopped || !data || typeof data !== "object" || !data.type) return;
      if (data.type === "subscribed") {
        window.clearTimeout(timer);
        if (!subscriptionSettled) {
          subscriptionSettled = true;
          subscribedSuccessfully = true;
          resolve();
        }
        return;
      }
      options.onEvent(data as T);
    });
    socket.addEventListener("error", () => {
      window.clearTimeout(timer);
      handleTransportFailure(reject);
    });
    socket.addEventListener("close", () => {
      window.clearTimeout(timer);
      handleTransportFailure(reject);
    });
  });
  return {
    ready,
    stop() {
      stopped = true;
      closeSocket();
    },
  };
}
