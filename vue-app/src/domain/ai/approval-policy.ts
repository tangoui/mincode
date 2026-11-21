import type { AiCapabilityRisk } from "./capability-registry";

export const AI_APPROVAL_MODES = ["ask", "agent", "full-access", "plan"] as const;

export type AiApprovalMode = (typeof AI_APPROVAL_MODES)[number];

export interface AiProtocolPermissionOption {
  optionId: string;
  kind?: string;
}

export const DEFAULT_AI_APPROVAL_MODE: AiApprovalMode = "ask";

export function normalizeAiApprovalMode(value: unknown): AiApprovalMode {
  return typeof value === "string" && AI_APPROVAL_MODES.includes(value as AiApprovalMode)
    ? value as AiApprovalMode
    : DEFAULT_AI_APPROVAL_MODE;
}

/**
 * Plan mode is read-only: every call whose resolved risk is not "read" is
 * refused before it runs or asks for approval.
 */
export function planModeDenies(mode: AiApprovalMode, risk: AiCapabilityRisk): boolean {
  return mode === "plan" && risk !== "read";
}

/**
 * Read-only capabilities never need approval. Request mode confirms every
 * side-effecting operation, agent mode only confirms dangerous capabilities,
 * and full-access mode deliberately bypasses tool approval for trusted workspaces.
 * Plan mode also confirms every side effect as a safety net; callers are
 * expected to refuse such calls first via planModeDenies.
 */
export function requiresAiToolApproval(mode: AiApprovalMode, risk: AiCapabilityRisk): boolean {
  if (risk === "read" || mode === "full-access") return false;
  if (mode === "agent") return risk === "dangerous";
  return true;
}

/** Map ACP tool categories onto the same risk model used by native tools. */
export function aiCapabilityRiskForProtocolTool(kind: unknown): AiCapabilityRisk {
  if (kind === "read" || kind === "search" || kind === "think") return "read";
  if (kind === "fetch") return "network";
  if (kind === "edit" || kind === "move") return "write";
  return "dangerous";
}

/**
 * Select an allow option when the active global policy permits an ACP operation
 * to continue. Prefer one-shot approval so changing the global mode takes effect
 * on the next protocol request instead of leaving an agent-owned permanent grant.
 * In plan mode non-read operations are rejected (again preferring one-shot) and
 * read operations are left to the user.
 */
export function automaticProtocolPermissionOption(
  mode: AiApprovalMode,
  toolKind: unknown,
  options: readonly AiProtocolPermissionOption[],
): string | undefined {
  const risk = aiCapabilityRiskForProtocolTool(toolKind);
  if (mode === "plan") {
    if (!planModeDenies(mode, risk)) return undefined;
    return options.find((option) => option.kind === "reject_once" && option.optionId)?.optionId
      ?? options.find((option) => option.kind === "reject_always" && option.optionId)?.optionId;
  }
  if (requiresAiToolApproval(mode, risk)) return undefined;
  return options.find((option) => option.kind === "allow_once" && option.optionId)?.optionId
    ?? options.find((option) => option.kind === "allow_always" && option.optionId)?.optionId;
}
