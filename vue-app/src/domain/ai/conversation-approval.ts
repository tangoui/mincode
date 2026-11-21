import type { AiApprovalMode } from "./approval-policy";

export function approvalModeForConversation(mode: AiApprovalMode, external: unknown): AiApprovalMode {
  return external && mode !== "plan" ? "ask" : mode;
}
