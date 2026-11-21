import type { AiAssistantMessage } from "@/stores/ai-assistant";

export interface ConversationExportLabels {
  user: string;
  assistant: string;
  error: string;
  attachments: string;
  exportedAt: string;
}

export interface ConversationExportInput {
  title: string;
  messages: readonly AiAssistantMessage[];
  exportedAt: Date;
  formatTime: (timestamp: number) => string;
  labels: ConversationExportLabels;
}

/** Renders a conversation's visible messages as a Markdown document. */
export function conversationToMarkdown(input: ConversationExportInput): string {
  const lines = [`# ${input.title.trim() || "AI"}`, "", `> ${input.labels.exportedAt}: ${input.formatTime(input.exportedAt.getTime())}`, ""];
  for (const message of input.messages) {
    if (message.eventAnchor && !message.content.trim()) continue;
    const role = message.role === "user" ? input.labels.user : input.labels.assistant;
    const time = typeof message.createdAt === "number" ? ` · ${input.formatTime(message.createdAt)}` : "";
    lines.push(`## ${role}${time}`, "");
    if (message.presentation === "error") {
      lines.push(...`${input.labels.error}: ${message.content.trim()}`.split("\n").map((line) => `> ${line}`));
    } else if (message.content.trim()) {
      lines.push(message.content.trim());
    }
    const names = (message.attachments ?? []).map((attachment) => attachment.name).filter(Boolean);
    if (names.length) lines.push("", `${input.labels.attachments}: ${names.join(", ")}`);
    lines.push("");
  }
  return `${lines.join("\n").trimEnd()}\n`;
}

/** A file name safe on every desktop OS, derived from the conversation title. */
export function conversationExportFileName(title: string) {
  const reserved = '\\/:*?"<>|';
  const cleaned = Array.from(title, (char) => (char.charCodeAt(0) < 0x20 || reserved.includes(char) ? " " : char)).join("");
  const base = cleaned.replace(/\s+/g, " ").trim().slice(0, 80);
  return `${base || "ai-conversation"}.md`;
}
