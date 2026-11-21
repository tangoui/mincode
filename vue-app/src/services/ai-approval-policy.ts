import { readonly, shallowRef } from "vue";
import {
  DEFAULT_AI_APPROVAL_MODE,
  normalizeAiApprovalMode,
  type AiApprovalMode,
} from "@/domain/ai/approval-policy";
import {
  loadSecurePersistedValue,
  saveSecurePersistedValue,
} from "@/services/secure-persistent-store";

export const AI_APPROVAL_POLICY_STORAGE_KEY = "app_ai_approval_policy_v1";

const approvalModeState = shallowRef<AiApprovalMode>(DEFAULT_AI_APPROVAL_MODE);
let hydrated = false;
let hydrationPromise: Promise<AiApprovalMode> | undefined;

export const aiApprovalMode = readonly(approvalModeState);

export async function hydrateAiApprovalMode(): Promise<AiApprovalMode> {
  if (hydrated) return approvalModeState.value;
  if (!hydrationPromise) {
    hydrationPromise = loadSecurePersistedValue<unknown>(AI_APPROVAL_POLICY_STORAGE_KEY)
      .then((stored) => {
        const value = stored && typeof stored === "object" && !Array.isArray(stored)
          ? (stored as Record<string, unknown>).mode
          : stored;
        approvalModeState.value = normalizeAiApprovalMode(value);
        hydrated = true;
        return approvalModeState.value;
      })
      .finally(() => {
        hydrationPromise = undefined;
      });
  }
  return await hydrationPromise;
}

/** Rejects when the native write fails; the in-memory mode only changes after it is persisted. */
export async function saveAiApprovalMode(mode: AiApprovalMode): Promise<void> {
  const normalized = normalizeAiApprovalMode(mode);
  await saveSecurePersistedValue(
    AI_APPROVAL_POLICY_STORAGE_KEY,
    { mode: normalized },
    { mode: normalized },
    { strict: true },
  );
  approvalModeState.value = normalized;
  hydrated = true;
}
