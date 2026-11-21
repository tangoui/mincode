import { readonly, shallowRef } from "vue";
import type {
  GlobalToolAuthorization,
  ToolAuthorization,
} from "@/domain/ai/tool-authorization";
import {
  loadSecurePersistedValue,
  saveSecurePersistedValue,
} from "@/services/secure-persistent-store";

export const TOOL_AUTHORIZATIONS_STORAGE_KEY = "app_ai_tool_authorizations_v1";

const globalToolAuthorizationsState = shallowRef<GlobalToolAuthorization[]>([]);
let hydrated = false;
let hydrationPromise: Promise<GlobalToolAuthorization[]> | undefined;

export const globalToolAuthorizations = readonly(globalToolAuthorizationsState);

function isGlobalAuthorization(value: unknown): value is GlobalToolAuthorization {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const grant = value as Record<string, unknown>;
  return grant.scope === "global"
    && typeof grant.toolName === "string"
    && grant.toolName.length > 0
    && typeof grant.resourceId === "string"
    && grant.resourceId.length > 0
    && typeof grant.parameterFingerprint === "string"
    && /^[0-9a-f]{64}$/.test(grant.parameterFingerprint)
    && (grant.risk === "read" || grant.risk === "network" || grant.risk === "write");
}

export async function loadGlobalToolAuthorizations(): Promise<GlobalToolAuthorization[]> {
  if (hydrated) return globalToolAuthorizationsState.value.slice();
  if (!hydrationPromise) {
    hydrationPromise = loadSecurePersistedValue<unknown>(TOOL_AUTHORIZATIONS_STORAGE_KEY)
      .then((stored) => {
        const globals = Array.isArray(stored) ? stored.filter(isGlobalAuthorization) : [];
        globalToolAuthorizationsState.value = globals;
        hydrated = true;
        return globals.slice();
      })
      .finally(() => {
        hydrationPromise = undefined;
      });
  }
  return await hydrationPromise;
}

/** Rejects when the native write fails; the in-memory list only changes after it is persisted. */
export async function saveGlobalToolAuthorizations(
  authorizations: readonly ToolAuthorization[]
): Promise<void> {
  const globals = authorizations.filter(isGlobalAuthorization);
  await saveSecurePersistedValue(TOOL_AUTHORIZATIONS_STORAGE_KEY, globals, globals, {
    strict: true,
  });
  globalToolAuthorizationsState.value = globals;
  hydrated = true;
}

export async function revokeGlobalToolAuthorization(
  authorization: GlobalToolAuthorization
): Promise<void> {
  const stored = await loadGlobalToolAuthorizations();
  await saveGlobalToolAuthorizations(stored.filter((item) =>
    item.toolName !== authorization.toolName
    || item.resourceId !== authorization.resourceId
    || item.parameterFingerprint !== authorization.parameterFingerprint
    || item.risk !== authorization.risk
  ));
}

export async function clearGlobalToolAuthorizations(): Promise<void> {
  await saveGlobalToolAuthorizations([]);
}
