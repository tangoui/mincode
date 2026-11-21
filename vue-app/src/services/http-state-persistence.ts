import { mergeLegacyHttpResourceStates, type HttpResourcesSnapshot } from "@/domain/http/resources";
import { loadAllPersistedValues } from "@/services/persistent-store";
import {
  loadSecurePersistedValue,
  saveSecurePersistedValue,
} from "@/services/secure-persistent-store";
import { createWorkspaceScopedPersistence } from "@/services/workspace-scoped-persistence";

export const HTTP_STATE_STORAGE_KEY = "app_http_state_v1";
export const HTTP_RESOURCES_STORAGE_KEY = "app_http_resources_v1";

type JsonRecord = Record<string, unknown>;

function isRecord(value: unknown): value is JsonRecord {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function redactRows(value: unknown) {
  if (!Array.isArray(value)) return value;
  return value.map((row) =>
    isRecord(row)
      ? { ...row, value: "", fileName: "", contentType: "" }
      : row
  );
}

function redactRequest(value: unknown) {
  if (!isRecord(value)) return value;
  return {
    ...value,
    url: "",
    authToken: "",
    authUsername: "",
    authPassword: "",
    apiKeyValue: "",
    oauth2AuthUrl: "",
    oauth2TokenUrl: "",
    oauth2ClientId: "",
    oauth2ClientSecret: "",
    oauth2Scope: "",
    oauth2CallbackUrl: "",
    oauth2Username: "",
    oauth2Password: "",
    oauth2AccessToken: "",
    params: redactRows(value.params),
    headers: redactRows(value.headers),
    formBody: redactRows(value.formBody),
    body: "",
    binaryFilePath: "",
    binaryFileName: "",
    graphqlQuery: "",
    graphqlVariables: "",
    documentation: "",
    response: undefined,
  };
}

function redactCollection(value: unknown): unknown {
  if (!isRecord(value)) return value;
  return {
    ...value,
    environments: Array.isArray(value.environments)
      ? value.environments.map((environment) =>
          isRecord(environment) ? { ...environment, vars: redactRows(environment.vars) } : environment
        )
      : value.environments,
    requests: Array.isArray(value.requests) ? value.requests.map(redactRequest) : value.requests,
    children: Array.isArray(value.children) ? value.children.map(redactCollection) : value.children,
  };
}

/** Browser preview retains structure but never persists request/environment values. */
export function sanitizeHttpStateForBrowser<T>(value: T): T {
  const snapshot = JSON.parse(JSON.stringify(value)) as unknown;
  if (!isRecord(snapshot)) return value;

  if (Array.isArray(snapshot.environments)) {
    snapshot.environments = snapshot.environments.map((environment) =>
      isRecord(environment) ? { ...environment, vars: redactRows(environment.vars) } : environment
    );
  }
  if (Array.isArray(snapshot.collections)) {
    snapshot.collections = snapshot.collections.map(redactCollection);
  }
  if (Array.isArray(snapshot.tabs)) {
    snapshot.tabs = snapshot.tabs.map(redactRequest);
  }
  if (Array.isArray(snapshot.history)) {
    snapshot.history = snapshot.history.map((entry) =>
      isRecord(entry) ? { ...entry, url: "" } : entry
    );
  }
  return snapshot as T;
}

export function createHttpStatePersistence<T>() {
  return createWorkspaceScopedPersistence<T>(HTTP_STATE_STORAGE_KEY, {
    secure: true,
    browserSnapshot: sanitizeHttpStateForBrowser,
  });
}

export function loadHttpState<T>(scopeId: string) {
  return createHttpStatePersistence<T>().load(scopeId);
}

export function saveHttpState<T>(scopeId: string, value: T) {
  return createHttpStatePersistence<T>().save(scopeId, value);
}

export async function loadOrMigrateHttpResources(): Promise<HttpResourcesSnapshot | null> {
  const current = await loadSecurePersistedValue<HttpResourcesSnapshot>(
    HTTP_RESOURCES_STORAGE_KEY
  );
  if (current) return current;

  const stored = await loadAllPersistedValues();
  const scopedPrefix = `${HTTP_STATE_STORAGE_KEY}__workspace__`;
  const legacyKeys = Object.keys(stored)
    .filter((key) => key === HTTP_STATE_STORAGE_KEY || key.startsWith(scopedPrefix))
    .sort();
  const legacyStates = await Promise.all(
    legacyKeys.map((key) => loadSecurePersistedValue<unknown>(key))
  );
  if (!legacyStates.some((state) => state !== null)) return null;
  const migrated = mergeLegacyHttpResourceStates(
    legacyStates.filter((state): state is unknown => state !== null)
  );
  await saveHttpResources(migrated);
  return migrated;
}

export function saveHttpResources(value: HttpResourcesSnapshot) {
  return saveSecurePersistedValue(
    HTTP_RESOURCES_STORAGE_KEY,
    value,
    sanitizeHttpStateForBrowser(value)
  );
}
