/** Socket.IO 面板最多保存的常用事件数量。 */
export const SOCKETIO_SAVED_EVENT_LIMIT = 100;
/** 单次发送的二进制附件数量与总大小上限，与后端保持一致。 */
export const SOCKETIO_MAX_ATTACHMENTS = 20;
export const SOCKETIO_MAX_ATTACHMENT_BYTES = 10 * 1024 * 1024;

const RESERVED_EVENTS = new Set([
  "connect",
  "connect_error",
  "disconnect",
  "disconnecting",
  "newListener",
  "removeListener",
]);

export interface SocketIOSavedEvent {
  id: string;
  event: string;
  args: string;
  ack: boolean;
  updatedAt: number;
}

export type SocketIOArgsCheck = { ok: true } | { ok: false; reason: "notArray" | "invalidJson" };

export function isReservedSocketIOEvent(event: string) {
  return RESERVED_EVENTS.has(event.trim());
}

/** 参数输入框为空视为无参数，否则必须是 JSON 数组。 */
export function checkSocketIOArgs(raw: string): SocketIOArgsCheck {
  if (!raw.trim()) return { ok: true };
  try {
    return Array.isArray(JSON.parse(raw)) ? { ok: true } : { ok: false, reason: "notArray" };
  } catch {
    return { ok: false, reason: "invalidJson" };
  }
}

/** 认证参数为空表示不带认证，否则必须是 JSON 对象。 */
export function isValidSocketIOAuth(raw: string) {
  if (!raw.trim()) return true;
  try {
    const value: unknown = JSON.parse(raw);
    return typeof value === "object" && value !== null && !Array.isArray(value);
  } catch {
    return false;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** 恢复持久化的常用事件：丢弃损坏条目，同名同参数去重，按最近使用排序并截断。 */
export function normalizeSocketIOSavedEvents(value: unknown): SocketIOSavedEvent[] {
  if (!Array.isArray(value)) return [];
  const seen = new Set<string>();
  const result: SocketIOSavedEvent[] = [];
  const items = value
    .filter(isRecord)
    .map((item) => ({
      id: typeof item.id === "string" && item.id ? item.id : "",
      event: typeof item.event === "string" ? item.event.trim() : "",
      args: typeof item.args === "string" ? item.args : "",
      ack: item.ack === true,
      updatedAt: typeof item.updatedAt === "number" && Number.isFinite(item.updatedAt) ? item.updatedAt : 0,
    }))
    .filter((item) => item.id && item.event && item.event.length <= 200 && !isReservedSocketIOEvent(item.event))
    .sort((left, right) => right.updatedAt - left.updatedAt);
  for (const item of items) {
    const key = `${item.event}\u0000${item.args.trim()}`;
    if (seen.has(key)) continue;
    seen.add(key);
    result.push(item);
    if (result.length >= SOCKETIO_SAVED_EVENT_LIMIT) break;
  }
  return result;
}

/** 保存或更新常用事件（同名同参数视为同一条，只刷新回执设置和时间）。 */
export function upsertSocketIOSavedEvent(
  items: readonly SocketIOSavedEvent[],
  input: { event: string; args: string; ack: boolean },
  createId: () => string,
  now = Date.now()
) {
  const event = input.event.trim();
  const existing = items.find((item) => item.event === event && item.args.trim() === input.args.trim());
  const saved: SocketIOSavedEvent = {
    id: existing?.id ?? createId(),
    event,
    args: input.args,
    ack: input.ack,
    updatedAt: now,
  };
  return normalizeSocketIOSavedEvents([saved, ...items.filter((item) => item.id !== saved.id)]);
}

/** 接收的参数以 JSON 数组展示；单个参数时直接展开，便于阅读。 */
export function formatSocketIOArgs(data: string) {
  try {
    const value: unknown = JSON.parse(data);
    const display = Array.isArray(value) && value.length === 1 ? value[0] : value;
    return JSON.stringify(display, null, 2) ?? "";
  } catch {
    return data;
  }
}
