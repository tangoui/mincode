import type { AgentRunPhase } from "@/domain/ai/session-progress";

const ACTIVE_PHASES = new Set<AgentRunPhase>(["queued", "thinking", "streaming", "tool_running", "waiting_approval", "waiting_input", "cancelling"]);

export interface AiRunStatusInput {
  waitingApproval: boolean;
  /** Synchronous copy of the last reported run phase and its user-facing summary. */
  live?: { phase: AgentRunPhase; summary: string };
  /** A tool still running in the current turn (local Agents report no tool phase). */
  runningToolTitle?: string;
}

export interface AiRunStatusLabels {
  processing: string;
  waitingApproval: string;
  runningTool: (tool: string) => string;
}

/** The one line shown at the end of a running conversation, saying what it is doing now. */
export function aiRunStatusText(input: AiRunStatusInput, labels: AiRunStatusLabels): string {
  if (input.waitingApproval) return labels.waitingApproval;
  if (input.runningToolTitle) return labels.runningTool(input.runningToolTitle);
  if (input.live && ACTIVE_PHASES.has(input.live.phase) && input.live.summary) return input.live.summary;
  return labels.processing;
}
