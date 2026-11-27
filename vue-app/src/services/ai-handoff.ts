import { ElMessage } from "element-plus";
import { shallowRef } from "vue";
import type { AiAttachment } from "@/domain/ai/attachments";
import { useVueI18n } from "@/i18n/messages";
import { redactSensitiveText } from "@/services/ai-tools/shared";
import { loadAiAssistantStore } from "@/stores/ai-assistant-shell";

export interface AiHandoffInput {
  source: "terminal" | "database" | "http" | "ssh" | "docker" | "notes";
  title: string;
  content: string;
  prompt: string;
  cwd?: string;
  projectRoot?: string;
}

interface AiHandoff {
  attachment: AiAttachment;
  prompt: string;
  projectRoot?: string;
}

const MAX_CONTEXT_LENGTH = 20_000;
const pending = shallowRef<AiHandoff[]>([]);
const { t } = useVueI18n();

export function hasPendingAiHandoffs() {
  return pending.value.length > 0;
}

export function takeAiHandoffs() {
  const handoffs = pending.value;
  pending.value = [];
  return handoffs;
}

export async function sendToAi(input: AiHandoffInput) {
  if (!input.content.trim()) return;
  // Redact before splitting so both ends of a secret cannot escape across the cut.
  const context = redactSensitiveText(input.content);
  const cwd = input.cwd?.trim();
  let data = cwd ? `${t("ai.handoff.cwd", { cwd: redactSensitiveText(cwd) })}\n\n${context}` : context;
  if (data.length > MAX_CONTEXT_LENGTH) {
    const marker = `\n\n[${t("ai.handoff.omitted")}]\n\n`;
    const head = Math.floor((MAX_CONTEXT_LENGTH - marker.length) / 2);
    const tail = MAX_CONTEXT_LENGTH - marker.length - head;
    data = data.slice(0, head).replace(/[\uD800-\uDBFF]$/, "") + marker
      + data.slice(-tail).replace(/^[\uDC00-\uDFFF]/, "");
  }

  const attachment: AiAttachment = {
    id: `handoff-${input.source}-${globalThis.crypto.randomUUID()}`,
    name: redactSensitiveText(input.title),
    kind: "text",
    mimeType: "text/plain",
    size: new TextEncoder().encode(data).length,
    data,
    origin: { kind: "handoff" },
  };
  try {
    const assistant = await loadAiAssistantStore();
    assistant.show();
    pending.value = [...pending.value, {
      attachment,
      prompt: redactSensitiveText(input.prompt),
      projectRoot: input.projectRoot?.trim(),
    }];
  } catch {
    ElMessage.error(t("ai.handoff.openFailed"));
  }
}
