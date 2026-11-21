import { invokeBackend } from "@/services/desktop-bridge";
import type { CloudPayloadEvent } from "@/services/cloud-payload-bridge";

export interface CloudAiSendPayload {
  conversationId?: string;
  content: string;
  createdAt?: number;
}

export function parseCloudAiSend(event: CloudPayloadEvent): CloudAiSendPayload | undefined {
  if (event.channel !== "ai.chat.send") return undefined;
  const content = typeof event.payload.content === "string" ? event.payload.content.trim() : "";
  if (!content) return undefined;
  return {
    conversationId: typeof event.payload.conversationId === "string"
      ? event.payload.conversationId.trim() || undefined
      : undefined,
    content,
    createdAt: typeof event.payload.createdAt === "number" ? event.payload.createdAt : undefined,
  };
}

export function acknowledgeCloudPayload(event: Pick<CloudPayloadEvent, "requestId" | "relayId">) {
  return invokeBackend<{ acknowledged: boolean }>("cloud_realtime_ack", {
    requestId: event.requestId,
    relayId: event.relayId,
  });
}

export function sendCloudAiResponse(input: {
  targetDeviceId: string;
  conversationId: string;
  requestId: string;
  content: string;
  status: string;
  sequence: number;
  /** 被回复的移动端请求 ID；流式片段与最终回复共用，移动端据此原位更新。 */
  replyTo?: string;
}) {
  return invokeBackend<{ queued: boolean }>("cloud_realtime_send", {
    requestId: input.requestId,
    targetDeviceId: input.targetDeviceId,
    channel: "ai.chat.response",
    sequence: input.sequence,
    payload: {
      conversationId: input.conversationId,
      content: input.content,
      status: input.status,
      replyTo: input.replyTo ?? input.requestId,
      createdAt: Date.now(),
    },
  });
}

export function parseCloudAiCancel(event: CloudPayloadEvent): string | undefined {
  if (event.channel !== "ai.chat.cancel") return undefined;
  const conversationId = typeof event.payload.conversationId === "string"
    ? event.payload.conversationId.trim()
    : "";
  return conversationId || undefined;
}

export function sendCloudAiSyncSnapshot(input: {
  targetDeviceId: string;
  requestId: string;
  sequence: number;
  payload: Record<string, unknown>;
}) {
  return invokeBackend<{ queued: boolean }>("cloud_realtime_send", {
    requestId: `${input.requestId}-snapshot`,
    targetDeviceId: input.targetDeviceId,
    channel: "ai.sync.snapshot",
    sequence: input.sequence,
    payload: input.payload,
  });
}


export function sendCloudApprovalRequest(input: {
  targetDeviceId: string;
  requestId: string;
  approvalId: string;
  conversationId: string;
  toolName: string;
  resourceId: string;
  risk: string;
  summary: string;
  sequence: number;
}) {
  return invokeBackend<{ queued: boolean }>("cloud_realtime_send", {
    requestId: input.requestId,
    targetDeviceId: input.targetDeviceId,
    channel: "approval.request",
    sequence: input.sequence,
    payload: {
      approvalId: input.approvalId,
      conversationId: input.conversationId,
      toolName: input.toolName,
      resourceId: input.resourceId,
      risk: input.risk,
      summary: input.summary,
      createdAt: Date.now(),
    },
  });
}
