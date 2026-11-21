/** Worker side of services/json-format.ts: pretty-prints one JSON body per message. */
import { formatJsonResponseBody } from "@/domain/http/response-body";

self.addEventListener("message", (event: MessageEvent) => {
  const data = event.data as { id?: unknown; body?: unknown } | undefined;
  if (typeof data?.id !== "number" || typeof data.body !== "string") return;
  self.postMessage({ id: data.id, text: formatJsonResponseBody(data.body) });
});
