// 流的前端门面：后端 core.StreamHandler 经 tango ipc 双向流承载（Go 侧
// internal/host/streamconn.go），页面拿到的是 WebSocket 形状的连接。
// 业务模块统一从这里导入，以保留 MinCode 的可用性判断和诊断上报。
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";
import { tangoRuntime, type TangoSocket } from "@/services/desktop-runtime";

export type { TangoSocket };

export function reportStreamDiagnostic(
  kind: "handshake_failure" | "fallback" | "error",
  error?: unknown,
): void {
  if (!isDesktopRuntime()) return;
  const message = error instanceof Error ? error.message : error == null ? "" : String(error);
  void invokeBackend("stream_diagnostics_report", { kind, error: message }).catch(() => undefined);
}

function observeStreamLifecycle<T extends { readyState: number; url: string } & EventTarget>(socket: T): T {
  let opened = socket.readyState === 1;
  socket.addEventListener("open", () => {
    opened = true;
  });
  socket.addEventListener("error", () => {
    reportStreamDiagnostic(opened ? "error" : "handshake_failure", `Stream ${socket.url} failed`);
  });
  return socket;
}

function openSocket(name: string): TangoSocket {
  const runtime = tangoRuntime();
  if (!runtime) throw new Error(`Stream ${name} requires the MinCode desktop runtime`);
  const socket = runtime.socket(name);
  socket.binaryType = "arraybuffer";
  return socket;
}

/** 字节流：send 字符串或字节，message 的 data 是 Go 发来的 ArrayBuffer。 */
export function Stream(name: string): TangoSocket {
  return observeStreamLifecycle(openSocket(name));
}

const decoder = new TextDecoder();

/**
 * 对象流：send(value) 发 JSON 文本（Go 侧 ReceiveJSON 解析），message 的 data
 * 是解析后的值（Go 的 SendJSON 和 Send 都发字节帧，这里按 JSON 文本解析）。
 */
export class JSONSocket extends EventTarget {
  readonly CONNECTING = 0;
  readonly OPEN = 1;
  readonly CLOSING = 2;
  readonly CLOSED = 3;

  private readonly socket: TangoSocket;

  constructor(socket: TangoSocket) {
    super();
    this.socket = socket;
    socket.addEventListener("open", () => this.dispatchEvent(new Event("open")));
    socket.addEventListener("error", () => this.dispatchEvent(new Event("error")));
    socket.addEventListener("close", (event) => {
      const e = event as CloseEvent;
      const init = { code: e.code, reason: e.reason, wasClean: e.wasClean };
      this.dispatchEvent(typeof CloseEvent === "function" ? new CloseEvent("close", init) : Object.assign(new Event("close"), init));
    });
    socket.addEventListener("message", (event) => {
      const raw = (event as MessageEvent).data;
      let data: unknown;
      try {
        data = JSON.parse(typeof raw === "string" ? raw : decoder.decode(raw as ArrayBuffer));
      } catch {
        this.dispatchEvent(new Event("error"));
        return;
      }
      this.dispatchEvent(new MessageEvent("message", { data }));
    });
  }

  addEventListener(type: "message", listener: (ev: MessageEvent) => void, options?: boolean | AddEventListenerOptions): void;
  addEventListener(type: string, listener: EventListenerOrEventListenerObject | null, options?: boolean | AddEventListenerOptions): void;
  addEventListener(type: string, listener: EventListenerOrEventListenerObject | ((ev: MessageEvent) => void) | null, options?: boolean | AddEventListenerOptions) {
    super.addEventListener(type, listener as EventListenerOrEventListenerObject | null, options);
  }

  removeEventListener(type: "message", listener: (ev: MessageEvent) => void, options?: boolean | EventListenerOptions): void;
  removeEventListener(type: string, listener: EventListenerOrEventListenerObject | null, options?: boolean | EventListenerOptions): void;
  removeEventListener(type: string, listener: EventListenerOrEventListenerObject | ((ev: MessageEvent) => void) | null, options?: boolean | EventListenerOptions) {
    super.removeEventListener(type, listener as EventListenerOrEventListenerObject | null, options);
  }

  get url() {
    return this.socket.url;
  }

  get readyState() {
    return this.socket.readyState;
  }

  get bufferedAmount() {
    return this.socket.bufferedAmount;
  }

  send(value: unknown) {
    this.socket.send(JSON.stringify(value));
  }

  close(code?: number, reason?: string) {
    this.socket.close(code, reason);
  }
}

export function JSONStream(name: string): JSONSocket {
  return observeStreamLifecycle(new JSONSocket(openSocket(name)));
}

// 流只在桌面运行时可用；浏览器预览可据此使用明确标注的非原生兼容路径。
export function isStreamAvailable(): boolean {
  return isDesktopRuntime();
}
