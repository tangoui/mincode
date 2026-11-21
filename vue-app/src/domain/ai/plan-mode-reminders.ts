import type { AiApprovalMode } from "./approval-policy";

// Model-facing protocol text: English and never localized.
export const PLAN_MODE_ENTER = "You are in Plan mode. Read and analyse only, then discuss a plan with the user. Do not modify files, run commands or call the network; such calls will be refused. Stay in Plan mode until the user switches modes.";
export const PLAN_MODE_LEAVE = "You are no longer in Plan mode. Earlier Plan mode restrictions no longer apply.";

/**
 * Returns a per-request reminder source: `enter` on every request while the mode
 * is plan, `leave` once on the first request after leaving plan, then nothing.
 * Each loop needs its own instance, since the leave notice is consumed on read.
 */
export function createPlanModeReminders(
  readMode: () => AiApprovalMode,
  texts: { enter: string; leave: string },
): () => string | undefined {
  let wasPlan = false;
  return () => {
    if (readMode() === "plan") {
      wasPlan = true;
      return texts.enter;
    }
    if (!wasPlan) return undefined;
    wasPlan = false;
    return texts.leave;
  };
}
