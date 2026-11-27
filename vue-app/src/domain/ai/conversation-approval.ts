import type { AiApprovalMode } from "./approval-policy";

export function approvalModeForConversation(mode: AiApprovalMode, external: unknown, fullAccess?: boolean): AiApprovalMode {
  if (fullAccess) return "full-access";
  return external && mode !== "plan" ? "ask" : mode;
}
