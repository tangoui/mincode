// 桌面运行时：tango 在页面里注入的 window.tango（即 @tangoui/runtime）。
//
// 前端与 Go 后端的全部往来都经这里：后端命令（mincode.invoke）、原生能力
// （mincode.native，见 internal/host/native.go）、事件（desktop-events.ts）、
// 流（desktop-stream.ts）。浏览器预览里没有 window.tango，isDesktopRuntime() 为 false。

/** tango 运行时里 MinCode 用到的部分（形状同 @tangoui/runtime）。 */
export interface TangoSocket extends EventTarget {
  readonly CONNECTING: 0;
  readonly OPEN: 1;
  readonly CLOSING: 2;
  readonly CLOSED: 3;
  readonly url: string;
  readonly readyState: number;
  readonly bufferedAmount: number;
  binaryType: "arraybuffer" | "blob";
  onopen: ((ev: Event) => void) | null;
  onmessage: ((ev: MessageEvent) => void) | null;
  onerror: ((ev: Event) => void) | null;
  onclose: ((ev: CloseEvent) => void) | null;
  send(data: string | ArrayBuffer | ArrayBufferView | Blob): void;
  close(code?: number, reason?: string): void;
  addEventListener(type: "message", listener: (ev: MessageEvent) => void, options?: boolean | AddEventListenerOptions): void;
  addEventListener(type: "close", listener: (ev: CloseEvent) => void, options?: boolean | AddEventListenerOptions): void;
  addEventListener(type: string, listener: EventListenerOrEventListenerObject | null, options?: boolean | AddEventListenerOptions): void;
  removeEventListener(type: "message", listener: (ev: MessageEvent) => void, options?: boolean | EventListenerOptions): void;
  removeEventListener(type: string, listener: EventListenerOrEventListenerObject | null, options?: boolean | EventListenerOptions): void;
}

export interface TangoStream<T> extends AsyncIterable<T> {
  cancel(): void;
}

export interface TangoRuntime {
  call<T = unknown>(method: string, ...args: unknown[]): Promise<T>;
  stream<T = unknown>(method: string, ...args: unknown[]): TangoStream<T>;
  socket(method: string, ...args: unknown[]): TangoSocket;
  emit(name: string, data?: unknown): void;
  onRaw(fn: (msg: string) => void): () => void;
}

/** 页面运行时；浏览器预览里为 null。 */
export function tangoRuntime(): TangoRuntime | null {
  const runtime = (globalThis as { tango?: TangoRuntime }).tango;
  return runtime && typeof runtime.call === "function" ? runtime : null;
}

/** 判断当前页面是否由 MinCode 桌面宿主（tango WebView）承载。 */
export function isDesktopRuntime(): boolean {
  return tangoRuntime() !== null;
}

function requireRuntime(): TangoRuntime {
  const runtime = tangoRuntime();
  if (!runtime) throw new Error("MinCode desktop runtime is unavailable");
  return runtime;
}

/** 调用原生能力（op 见 internal/host/native.go）。对话框被取消时结果为 null。 */
export function nativeCall<T = unknown>(op: string, args?: unknown): Promise<T> {
  return requireRuntime().call<T>("mincode.native", { op, args });
}

/**
 * 调用后端命令，结果是命令返回的 JSON 值。
 *
 * 带 signal 时以只回一帧的流承载：中止即取消流，Go 侧命令的 ctx 随之取消，
 * promise 以 signal.reason 拒绝（ipc 的普通调用不能取消）。
 */
export async function invokeCommand<T>(
  command: string,
  args: Record<string, unknown>,
  signal?: AbortSignal,
): Promise<T> {
  const runtime = requireRuntime();
  const request = { command, args };
  if (!signal) return (await runtime.call<T>("mincode.invoke", request)) ?? (undefined as T);
  signal.throwIfAborted();
  const stream = runtime.stream<T>("mincode.invokeStream", request);
  const abort = () => stream.cancel();
  signal.addEventListener("abort", abort, { once: true });
  try {
    for await (const value of stream) return value ?? (undefined as T);
    signal.throwIfAborted();
    return undefined as T;
  } catch (error) {
    signal.throwIfAborted();
    throw error;
  } finally {
    signal.removeEventListener("abort", abort);
  }
}
