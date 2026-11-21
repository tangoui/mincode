// 终端数据面 Stream 客户端（tango Streams）。
//
// 复用后端 terminal/wire_frame.go 的 Wire v1 帧格式：
//   [version:1][type:1][length:4 BE][payload]
// 经资产服务器承载，不占 TCP 端口、无需 token 认证；浏览器预览不可用时返回 null，
// 调用方回退到原有 write_to_shell/shell-output 事件通道。
import { parseWireHandshakeError } from "@/domain/terminal/wire-handshake";
import { isStreamAvailable, reportStreamDiagnostic, Stream } from "@/services/desktop-stream";

const WIRE_VERSION = 1;
const MSG_DATA = 1;
const MSG_RESIZE = 2;
const MSG_HEARTBEAT = 3;

const textEncoder = new TextEncoder();
const textDecoder = new TextDecoder();

export interface TerminalWireHandle {
  sendInput(text: string): boolean;
  sendResize(cols: number, rows: number): boolean;
  close(): void;
}

function encodeFrame(type: number, payload: Uint8Array): ArrayBuffer {
  const frame = new Uint8Array(6 + payload.length);
  frame[0] = WIRE_VERSION;
  frame[1] = type;
  const n = payload.length;
  frame[2] = (n >>> 24) & 0xff;
  frame[3] = (n >>> 16) & 0xff;
  frame[4] = (n >>> 8) & 0xff;
  frame[5] = n & 0xff;
  frame.set(payload, 6);
  return frame.buffer;
}

function decodeFrame(buffer: ArrayBuffer): { type: number; payload: Uint8Array } | null {
  const view = new Uint8Array(buffer);
  if (view.length < 6 || view[0] !== WIRE_VERSION) return null;
  const len = (view[2] << 24) | (view[3] << 16) | (view[4] << 8) | view[5];
  if (len < 0 || view.length < 6 + len) return null;
  return { type: view[1], payload: view.subarray(6, 6 + len) };
}

/**
 * 打开指定会话的终端数据流。
 * @param onOutput 收到 shell 输出（msgData）时回调
 * @param onState  通道可用状态变化回调（true=输出走流，事件通道应跳过该会话）
 * @returns 句柄；桌面运行时不可用时返回 null
 */
export function openTerminalWire(
  sessionId: string,
  onOutput: (data: string) => void,
  onState?: (active: boolean) => void
): TerminalWireHandle | null {
  if (!isStreamAvailable()) return null;

  const socket = Stream("shell-wire");
  let active = false;
  let closed = false;
  let opened = false;
  let fallbackReported = false;
  let handshakeRejection = "";
  const reportFallback = (error?: string) => {
    if (fallbackReported) return;
    fallbackReported = true;
    reportStreamDiagnostic("fallback", error);
  };
  const handshakeTimer = window.setTimeout(() => {
    if (closed || active) return;
    reportStreamDiagnostic("handshake_failure", `Terminal Stream handshake timed out: ${sessionId}`);
    reportFallback();
  }, 5000);

  const setActive = (next: boolean) => {
    if (active === next || closed) return;
    active = next;
    onState?.(next);
  };

  socket.addEventListener("open", () => {
    opened = true;
    // 首帧 JSON 握手：告知后端目标会话。
    socket.send(JSON.stringify({ sessionId }));
  });
  socket.addEventListener("message", ((event: MessageEvent) => {
    const data = event.data;
    if (!(data instanceof ArrayBuffer) && typeof data !== "string") return;
    const frame = typeof data === "string" ? null : decodeFrame(data);
    if (!frame) {
      // 后端握手期可能回 JSON 拒绝帧（如 session not found），提取原因并入诊断。
      if (!active) {
        const rejection = parseWireHandshakeError(data);
        if (rejection) handshakeRejection = rejection;
      }
      return;
    }
    if (frame.type === MSG_DATA) {
      onOutput(textDecoder.decode(frame.payload));
    } else if (frame.type === MSG_HEARTBEAT) {
      window.clearTimeout(handshakeTimer);
      setActive(true);
    }
  }) as EventListener);
  const handleDown = () => {
    if (closed) return;
    window.clearTimeout(handshakeTimer);
    if (!active) {
      const reason = handshakeRejection
        ? `Terminal Stream rejected (${handshakeRejection}): ${sessionId}`
        : `Terminal Stream closed before handshake: ${sessionId}`;
      if (opened) reportStreamDiagnostic("handshake_failure", reason);
      reportFallback(handshakeRejection ? reason : undefined);
    } else {
      reportFallback(`Terminal Stream disconnected: ${sessionId}`);
    }
    setActive(false);
    try {
      socket.close();
    } catch {
      // 连接可能已关闭
    }
  };
  socket.addEventListener("close", handleDown);
  socket.addEventListener("error", handleDown);

  return {
    sendInput(text: string) {
      if (!active || closed || socket.readyState !== socket.OPEN) return false;
      try {
        socket.send(encodeFrame(MSG_DATA, textEncoder.encode(text)));
        return true;
      } catch {
        handleDown();
        return false;
      }
    },
    sendResize(cols: number, rows: number) {
      if (!active || closed || socket.readyState !== socket.OPEN) return false;
      const payload = new Uint8Array(4);
      payload[0] = (cols >>> 8) & 0xff;
      payload[1] = cols & 0xff;
      payload[2] = (rows >>> 8) & 0xff;
      payload[3] = rows & 0xff;
      try {
        socket.send(encodeFrame(MSG_RESIZE, payload));
        return true;
      } catch {
        handleDown();
        return false;
      }
    },
    close() {
      if (closed) return;
      closed = true;
      window.clearTimeout(handshakeTimer);
      setActive(false);
      try {
        socket.close();
      } catch {
        // 连接可能已关闭
      }
    },
  };
}
