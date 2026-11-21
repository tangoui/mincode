import { invokeBackend } from "@/services/desktop-bridge";
import type { PluginAiTurnRequest, PluginAiTurnStatus } from "@/domain/ai/plugin-turns";

// Turn ids announced by the host ("plugin:ai-turn") wait here until the AI
// drawer — which owns the agent loop and may still be loading — claims them.
const pending = new Set<string>();
const turnListeners = new Set<() => void>();
const cancelListeners = new Set<(id: string) => void>();
const approvalListeners = new Set<(decision: PluginAiTurnApprovalDecision) => void>();

export interface PluginAiTurnApprovalDecision {
  id: string;
  approvalId: string;
  approved: boolean;
}

/** A chat decision relayed by the host for a ChatApproval turn. */
export function notifyPluginAiTurnApproval(event: unknown) {
  const value = event as Partial<PluginAiTurnApprovalDecision> | undefined;
  if (typeof value?.id !== "string" || typeof value.approvalId !== "string" || typeof value.approved !== "boolean") return;
  const decision = { id: value.id, approvalId: value.approvalId, approved: value.approved };
  for (const listener of approvalListeners) listener(decision);
}

export function subscribePluginAiTurnApprovals(listener: (decision: PluginAiTurnApprovalDecision) => void) {
  approvalListeners.add(listener);
  return () => approvalListeners.delete(listener);
}

export function enqueuePluginAiTurn(id: unknown) {
  if (typeof id !== "string" || !id.startsWith("aiturn-") || pending.has(id)) return false;
  pending.add(id);
  for (const listener of turnListeners) listener();
  return true;
}

export function takePendingPluginAiTurns() {
  const ids = [...pending];
  pending.clear();
  return ids;
}

export function subscribePluginAiTurns(listener: () => void) {
  turnListeners.add(listener);
  return () => turnListeners.delete(listener);
}

export function notifyPluginAiTurnCancelled(id: unknown) {
  if (typeof id !== "string") return;
  pending.delete(id);
  for (const listener of cancelListeners) listener(id);
}

export function subscribePluginAiTurnCancellations(listener: (id: string) => void) {
  cancelListeners.add(listener);
  return () => cancelListeners.delete(listener);
}

/** Turns announced before the listener was attached (app start, drawer still loading). */
export async function refreshPendingPluginAiTurns() {
  const turns = await invokeBackend<Array<Pick<PluginAiTurnRequest, "id">>>("plugin_ai_turn_pending", {});
  let added = false;
  for (const turn of turns ?? []) added = enqueuePluginAiTurn(turn?.id) || added;
  return added;
}

export function claimPluginAiTurn(id: string) {
  return invokeBackend<PluginAiTurnRequest>("plugin_ai_turn_claim", { id });
}

export interface PluginAiTurnUpdate {
  id: string;
  status?: PluginAiTurnStatus;
  text?: string;
  activity?: string;
  error?: string;
  conversationId?: string;
  approval?: { id: string; summary: string; risk: string; tool: string };
}

export function updatePluginAiTurn(update: PluginAiTurnUpdate) {
  return invokeBackend<boolean>("plugin_ai_turn_update", { ...update });
}
