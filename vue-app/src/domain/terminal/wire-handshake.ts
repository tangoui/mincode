// 终端 Wire 流握手拒绝帧解析（纯函数，供 terminal-wire 客户端与测试共用）。
//
// shell-wire 流在握手阶段由后端 shellWireStreamHandler 校验会话：
// 会话不存在时（派生会话秒退、SSH 宽限期过期等）后端回
// JSON {"error":"session not found"} 并关闭连接。该帧不是 Wire v1 二进制帧，
// 解析失败的帧应用本函数提取拒绝原因，让诊断面板展示真实原因而非
// 笼统的 "closed before handshake"。
export function parseWireHandshakeError(data: ArrayBuffer | string): string | null {
  try {
    const text = typeof data === "string" ? data : new TextDecoder().decode(data);
    const value = JSON.parse(text) as { error?: unknown };
    const error = value?.error;
    if (typeof error !== "string" || !error.trim()) return null;
    return error.trim();
  } catch {
    return null;
  }
}
