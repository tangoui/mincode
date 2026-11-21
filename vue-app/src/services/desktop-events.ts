// 应用事件：Go → 页面按序下发，页面之间经 Go 广播。
//
// 下行协议（Go 侧 internal/host/events.go）：tango Surface.Send 发原始消息，
//   "wev:"  + {name, data} 的 JSON   小事件内联
//   "wref:" + id                     大事件按引用，载荷 GET /_mincode/event-ref?id= 取回
// 本模块在加载时经运行时 onRaw 订阅（之前到达的消息由运行时缓存），按到达顺序分发：
// 有引用在途时，后面的事件（含内联事件）排在它后面，页面看到的顺序与 Go 的 Emit 一致；
// 没有引用在途时同步分发。监听器抛错或取引用失败只报告，不会卡住后面的事件。
import { tangoRuntime } from "@/services/desktop-runtime";

const MSG_INLINE = "wev:";
const MSG_REF = "wref:";
const EVENT_REF_PATH = "/_mincode/event-ref";

interface AppEvent {
  name: string;
  data: unknown;
}

type QueueItem = AppEvent | { ref: string };
type Listener = (data: unknown) => void;

const listeners = new Map<string, Set<Listener>>();
const queue: QueueItem[] = [];
let draining = false;

// 报告但不中断分发：reportError 与未捕获异常一样进控制台和 window.onerror。
function report(error: unknown) {
  if (typeof globalThis.reportError === "function") {
    globalThis.reportError(error);
    return;
  }
  setTimeout(() => {
    throw error;
  });
}

function deliver(event: AppEvent) {
  const set = listeners.get(event.name);
  if (!set) return;
  for (const fn of [...set]) {
    try {
      fn(event.data);
    } catch (error) {
      report(error);
    }
  }
}

async function fetchRef(id: string): Promise<AppEvent> {
  const response = await fetch(`${EVENT_REF_PATH}?id=${encodeURIComponent(id)}`, { cache: "no-store" });
  if (!response.ok) throw new Error(`event-ref ${response.status}`);
  return (await response.json()) as AppEvent;
}

// async 函数在第一个 await 之前同步执行：没有引用在途时，内联事件在 push 里即刻分发。
async function drain() {
  try {
    while (queue.length) {
      const item = queue.shift()!;
      let event: AppEvent;
      if ("ref" in item) {
        try {
          event = await fetchRef(item.ref);
        } catch (error) {
          report(error);
          continue;
        }
      } else {
        event = item;
      }
      deliver(event);
    }
  } finally {
    draining = false;
  }
}

function push(item: QueueItem) {
  queue.push(item);
  if (draining) return;
  draining = true;
  void drain();
}

/** 处理一条下行原始消息；不认识的消息忽略。导出供测试。 */
export function handleRawMessage(msg: string) {
  if (typeof msg !== "string") return;
  if (msg.startsWith(MSG_INLINE)) {
    push(JSON.parse(msg.slice(MSG_INLINE.length)) as AppEvent);
  } else if (msg.startsWith(MSG_REF)) {
    push({ ref: msg.slice(MSG_REF.length) });
  }
}

tangoRuntime()?.onRaw(handleRawMessage);

/** 订阅应用事件，返回取消订阅函数。 */
export function onEvent(name: string, fn: Listener): () => void {
  let set = listeners.get(name);
  if (!set) listeners.set(name, (set = new Set()));
  set.add(fn);
  return () => {
    set.delete(fn);
    if (!set.size && listeners.get(name) === set) listeners.delete(name);
  };
}

/** 向所有窗口（含本页）广播应用事件。 */
export function emitEvent(name: string, data: unknown) {
  tangoRuntime()?.emit("mincode.emit", { name, data: data === undefined ? null : data });
}
