import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";

async function importTypeScriptSource(path, transform = (source) => source) {
  const source = transform(await readFile(new URL(path, import.meta.url), "utf8"));
  const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
  }).outputText;
  return import(`data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}`);
}

const configSync = await importTypeScriptSource(
  "../src/domain/settings/config-sync.ts",
  (source) =>
    source.replace(
      'import { decryptAes, encryptAes } from "@/domain/security/crypto";',
      `const encryptAes = async (value, password) =>
        \`test:\${encodeURIComponent(password)}:\${encodeURIComponent(value)}\`;
       const decryptAes = async (payload, password) => {
         const prefix = \`test:\${encodeURIComponent(password)}:\`;
         if (!payload.startsWith(prefix)) throw new Error("Decryption failed");
         return decodeURIComponent(payload.slice(prefix.length));
       };`
    )
);

globalThis.__settingsPersistenceMocks = {
  saved: [],
  saveFailure: null,
};

const settingsPersistence = await importTypeScriptSource(
  "../src/services/settings-persistence.ts",
  (source) =>
    source
      .replace(
        'import { shallowRef } from "vue";',
        "const shallowRef = (value) => ({ value });"
      )
      .replace(
        /import \{[\s\S]*?\} from "@\/domain\/performance\/runtime-limits";/,
        `const RUNTIME_PERFORMANCE_SETTING_KEYS = [
          "performanceProfile", "lspBackgroundStopSeconds", "lspIdleStopSeconds",
          "lspAuxiliaryIdleStopSeconds", "aiInactiveWorkspaceCacheCount",
          "aiInactiveWorkspaceCacheMiB", "terminalLayoutCacheLimit"
        ];`
      )
      .replace(
        /import \{[\s\S]*?\} from "@\/services\/secure-persistent-store";/,
        // Mirrors secure-persistent-store: failures are swallowed unless `strict`.
        `const loadSecurePersistedValue = async () => null;
         const saveSecurePersistedValue = async (...args) => {
           globalThis.__settingsPersistenceMocks.saved.push(args);
           const failure = globalThis.__settingsPersistenceMocks.saveFailure;
           if (failure && args[3]?.strict) throw failure;
         };`
      )
      // Desktop semantics: browser-local AI keys (sessionStorage) are not involved.
      .replace('import { storeBrowserAiKeys, withBrowserAiKeys } from "@/services/browser-ai-keys";', "")
      .replace('import { isDesktopRuntime } from "@/services/desktop-bridge";', "const isDesktopRuntime = () => true;")
);

test("automatic settings persistence updates runtime and stays best-effort on native failure", async () => {
  globalThis.__settingsPersistenceMocks.saved = [];
  globalThis.__settingsPersistenceMocks.saveFailure = new Error("native failed");
  const beforeVersion = settingsPersistence.settingsSnapshotVersion.value;

  await settingsPersistence.persistSettingsSnapshot({ uiTheme: "dark", apiKey: "secret" });
  assert.equal(settingsPersistence.settingsSnapshotVersion.value, beforeVersion + 1);
  assert.deepEqual(settingsPersistence.readSettingsSnapshot(), {
    uiTheme: "dark",
    apiKey: "secret",
  });
  assert.deepEqual(globalThis.__settingsPersistenceMocks.saved.at(-1)?.[3], {});
  globalThis.__settingsPersistenceMocks.saveFailure = null;
});

test("explicit settings save passes strict and rejects on native failure", async () => {
  globalThis.__settingsPersistenceMocks.saved = [];
  globalThis.__settingsPersistenceMocks.saveFailure = new Error("native failed");

  await assert.rejects(
    settingsPersistence.persistSettingsSnapshot({ uiTheme: "light" }, { strict: true }),
    /native failed/
  );
  assert.deepEqual(globalThis.__settingsPersistenceMocks.saved.at(-1)?.[3], { strict: true });
  globalThis.__settingsPersistenceMocks.saveFailure = null;
});

test("normalizes repository defaults and recognizes automatically synced keys", () => {
  assert.deepEqual(configSync.normalizeConfigSyncRepository({ authMode: "invalid" }), {
    remoteUrl: "",
    branch: "main",
    authMode: "system",
    username: "",
    token: "",
    encryptionPassword: "",
    rememberPassword: false,
    lastSyncAt: undefined,
    lastCommit: undefined,
  });

  assert.equal(configSync.isConfigSyncStorageKey("app_future_feature_v1"), true);
  assert.equal(configSync.isConfigSyncStorageKey("app_config_sync_repository_v1"), false);
  assert.equal(configSync.isConfigSyncStorageKey("unrelated_key"), false);
});

test("encodes and validates a v2 all-persistence snapshot", async () => {
  const snapshot = {
    format: configSync.CONFIG_SYNC_FORMAT,
    version: configSync.CONFIG_SYNC_VERSION,
    exportedAt: 123,
    persistedValues: {
      app_settings_v2: { storage: "secure", value: { uiTheme: "dark" } },
      app_future_feature_v1: { storage: "plain", value: { enabled: true } },
    },
  };
  const payload = await configSync.encodeConfigSyncSnapshot(snapshot, "password-123");
  const decoded = await configSync.decodeConfigSyncSnapshot(payload, "password-123");
  assert.deepEqual(decoded, snapshot);
  await assert.rejects(
    configSync.encodeConfigSyncSnapshot(snapshot, "short"),
    /config_sync_password_too_short/
  );

  assert.throws(
    () =>
      configSync.validateConfigSyncSnapshot({
        format: configSync.CONFIG_SYNC_FORMAT,
        version: 3,
        persistedValues: {},
      }),
    /config_sync_unsupported_version/
  );
  assert.throws(
    () =>
      configSync.validateConfigSyncSnapshot({
        ...snapshot,
        persistedValues: {
          app_config_sync_repository_v1: { storage: "secure", value: {} },
        },
      }),
    /config_sync_invalid_payload/
  );
});

test("converts legacy v1 snapshots to persisted v2 entries", () => {
  const converted = configSync.validateConfigSyncSnapshot({
    format: configSync.CONFIG_SYNC_FORMAT,
    version: 1,
    exportedAt: 99,
    settings: { uiTheme: "light" },
    sidebarVisibility: { terminal: true, ignored: "yes" },
    workspaceStateIsolationEnabled: false,
  });

  assert.deepEqual(converted, {
    format: configSync.CONFIG_SYNC_FORMAT,
    version: configSync.CONFIG_SYNC_VERSION,
    exportedAt: 99,
    persistedValues: {
      app_settings_v2: { storage: "secure", value: { uiTheme: "light" } },
      app_sidebar_visibility_v1: { storage: "plain", value: { terminal: true } },
      app_workspace_state_preferences_v1: {
        storage: "plain",
        value: { version: 1, isolationEnabled: false },
      },
    },
  });
});

test("redacts portable credentials and restores only local secret fields", () => {
  const sanitized = configSync.sanitizeConfigSyncValue({
    model: "gpt-5",
    maxOutputTokens: 4096,
    password: "remote-password",
    nested: { apiKey: "remote-key", cipher: "enc:v2:device-bound" },
  });
  assert.deepEqual(sanitized, {
    model: "gpt-5",
    maxOutputTokens: 4096,
    password: "",
    nested: { apiKey: "", cipher: "" },
  });

  const merged = configSync.mergeConfigSyncValueWithLocalSecrets(
    {
      profiles: [
        { id: "existing", model: "remote", token: "remote-must-not-win" },
        { id: "new", model: "new", token: "remote-must-not-import" },
      ],
    },
    {
      profiles: [{ id: "existing", model: "local", token: "local-token" }],
    }
  );
  assert.deepEqual(merged, {
    profiles: [
      { id: "existing", model: "remote", token: "local-token" },
      { id: "new", model: "new", token: "" },
    ],
  });
});

test("remote settings retain only matching device-local secrets", () => {
  const merged = settingsPersistence.mergeSettingsWithLocalSecrets(
    {
      uiTheme: "dark",
      performanceProfile: "high-performance",
      lspIdleStopSeconds: 600,
      aiProfiles: [
        { id: "existing", apiKey: "remote-must-not-win", model: "gpt" },
        { id: "new", apiKey: "remote-must-not-import", model: "other" },
      ],
      mcpServers: [
        {
          id: "mcp-existing",
          env: { TOKEN: "remote-must-not-win", NEW_SECRET: "remote-value" },
          headers: { Authorization: "remote-must-not-win" },
        },
      ],
    },
    {
      uiTheme: "light",
      performanceProfile: "low-memory",
      lspIdleStopSeconds: 60,
      aiProfiles: [{ id: "existing", apiKey: "local-api-key" }],
      mcpServers: [
        {
          id: "mcp-existing",
          env: { TOKEN: "local-token" },
          headers: { Authorization: "local-header" },
        },
      ],
    }
  );

  assert.equal(merged.uiTheme, "dark");
  assert.equal(merged.performanceProfile, "low-memory");
  assert.equal(merged.lspIdleStopSeconds, 60);
  assert.deepEqual(merged.aiProfiles, [
    { id: "existing", apiKey: "local-api-key", model: "gpt" },
    { id: "new", apiKey: "", model: "other" },
  ]);
  assert.deepEqual(merged.mcpServers, [
    {
      id: "mcp-existing",
      env: { TOKEN: "local-token", NEW_SECRET: "" },
      headers: { Authorization: "local-header" },
    },
  ]);
});

const persistenceState = {
  stored: {},
  secure: {},
  preparedSecure: [],
  batches: [],
  runtimeSettings: [],
  prepareFailure: null,
  batchFailure: null,
  desktopRuntime: false,
};
globalThis.__configSyncPersistenceMocks = {
  domain: configSync,
  desktop: {
    isDesktopRuntime: () => persistenceState.desktopRuntime,
  },
  http: {
    HTTP_RESOURCES_STORAGE_KEY: "app_http_resources_v1",
    HTTP_STATE_STORAGE_KEY: "app_http_state_v1",
    sanitizeHttpStateForBrowser: (value) => value,
  },
  ai: {
    isRetiredAiHistoryStorageKey: (key) => key === "app_ai_conversations_v2"
      || key.startsWith("app_ai_conversations_v2__")
      || key === "app_ai_session_v1"
      || key.startsWith("app_ai_session_v1__"),
  },
  persistent: {
    loadAllPersistedValues: async () => persistenceState.stored,
    applyPersistedBatch: async (batch) => {
      if (persistenceState.batchFailure) throw persistenceState.batchFailure;
      persistenceState.batches.push(batch);
    },
  },
  secure: {
    loadSecurePersistedValue: async (key) => persistenceState.secure[key] ?? null,
    prepareSecurePersistedValue: async (value) => {
      if (persistenceState.prepareFailure) throw persistenceState.prepareFailure;
      persistenceState.preparedSecure.push(value);
      return `enc:v2:prepared:${persistenceState.preparedSecure.length}`;
    },
  },
  settings: {
    SETTINGS_STORAGE_KEY: "app_settings_v2",
    sanitizeSettingsForSync: settingsPersistence.sanitizeSettingsForSync,
    mergeSettingsWithLocalSecrets: settingsPersistence.mergeSettingsWithLocalSecrets,
    updateSettingsRuntimeSnapshot: (value) => {
      persistenceState.runtimeSettings.push(value);
    },
  },
};

const configSyncPersistence = await importTypeScriptSource(
  "../src/services/config-sync-persistence.ts",
  (source) =>
    source
      .replace(
        /import \{[\s\S]*?\} from "@\/domain\/settings\/config-sync";/,
        `const {
          CONFIG_SYNC_FORMAT, CONFIG_SYNC_VERSION, isConfigSyncStorageKey,
          mergeConfigSyncValueWithLocalSecrets, sanitizeConfigSyncValue
        } = globalThis.__configSyncPersistenceMocks.domain;`
      )
      .replace(
        /import \{[\s\S]*?\} from "@\/services\/http-state-persistence";/,
        `const {
          HTTP_RESOURCES_STORAGE_KEY, HTTP_STATE_STORAGE_KEY, sanitizeHttpStateForBrowser
        } = globalThis.__configSyncPersistenceMocks.http;`
      )
      .replace(
        /import \{[\s\S]*?\} from "@\/services\/persistent-store";/,
        `const {
          applyPersistedBatch, loadAllPersistedValues
        } = globalThis.__configSyncPersistenceMocks.persistent;`
      )
      .replace(
        /import \{[\s\S]*?\} from "@\/services\/secure-persistent-store";/,
        `const {
          loadSecurePersistedValue, prepareSecurePersistedValue
        } = globalThis.__configSyncPersistenceMocks.secure;`
      )
      .replace(
        /import \{[\s\S]*?\} from "@\/services\/ai-conversation-persistence";/,
        `const {
          isRetiredAiHistoryStorageKey
        } = globalThis.__configSyncPersistenceMocks.ai;`
      )
      .replace(
        /import \{[\s\S]*?\} from "@\/services\/plugin-configuration-service";/,
        `const {
          exportPluginConfigurationSync, importPluginConfigurationSync
        } = globalThis.__configSyncPersistenceMocks.pluginConfigurationService;`
      )
      .replace(
        'import { isDesktopRuntime } from "@/services/desktop-bridge";',
        "const { isDesktopRuntime } = globalThis.__configSyncPersistenceMocks.desktop;"
      )
      .replace(
        /import \{[\s\S]*?\} from "@\/services\/settings-persistence";/,
        `const {
          mergeSettingsWithLocalSecrets, sanitizeSettingsForSync,
          SETTINGS_STORAGE_KEY, updateSettingsRuntimeSnapshot
        } = globalThis.__configSyncPersistenceMocks.settings;`
      )
);

test("collects every app_ key automatically while excluding sync-owned and credential stores", async () => {
  persistenceState.stored = {
    app_settings_v2: "enc:v2:settings",
    app_future_feature_v1: { enabled: true, token: "must-redact" },
    app_future_secure_v1: "enc:v2:future",
    app_config_sync_repository_v1: "enc:v2:repository",
    app_license: "enc:v2:license",
    app_ssh_credentials_v1: { credentialsByProfileId: {} },
    app_ai_conversations_v2: { conversations: [{ id: "retired" }] },
    unrelated_key: { ignored: true },
  };
  persistenceState.secure = {
    app_settings_v2: { uiTheme: "dark", aiProfiles: [{ id: "a", apiKey: "secret" }] },
    app_future_secure_v1: { enabled: true, password: "secret" },
  };

  const snapshot = await configSyncPersistence.createConfigSyncSnapshot();
  assert.deepEqual(Object.keys(snapshot.persistedValues).sort(), [
    "app_future_feature_v1",
    "app_future_secure_v1",
    "app_settings_v2",
  ]);
  assert.deepEqual(snapshot.persistedValues.app_future_feature_v1, {
    storage: "plain",
    value: { enabled: true, token: "" },
  });
  assert.deepEqual(snapshot.persistedValues.app_future_secure_v1, {
    storage: "secure",
    value: { enabled: true, password: "" },
  });
  assert.deepEqual(snapshot.persistedValues.app_settings_v2.value, {
    uiTheme: "dark",
    aiProfiles: [{ id: "a", apiKey: "" }],
  });
});

test("skips retired AI history keys when applying synced configuration", async () => {
  persistenceState.stored = {};
  persistenceState.batches = [];

  const result = await configSyncPersistence.applyConfigSyncSnapshot({
    format: configSync.CONFIG_SYNC_FORMAT,
    version: configSync.CONFIG_SYNC_VERSION,
    exportedAt: 1,
    persistedValues: {
      app_ai_conversations_v2: { storage: "plain", value: { conversations: [] } },
      app_future_feature_v1: { storage: "plain", value: { enabled: true } },
    },
  });

  assert.deepEqual(result.skippedKeys, ["app_ai_conversations_v2"]);
  assert.deepEqual(persistenceState.batches, [{ sets: [{
    key: "app_future_feature_v1",
    value: { enabled: true },
    browserSnapshot: { enabled: true },
  }] }]);
});

test("does not downgrade an existing secure key when a snapshot labels it plain", async () => {
  persistenceState.stored = {
    app_future_secure_v1: "enc:v2:future",
  };
  persistenceState.secure = {
    app_future_secure_v1: { id: "secure", enabled: false, password: "local-password" },
  };
  persistenceState.preparedSecure = [];
  persistenceState.batches = [];
  persistenceState.runtimeSettings = [];

  await configSyncPersistence.applyConfigSyncSnapshot({
    format: configSync.CONFIG_SYNC_FORMAT,
    version: configSync.CONFIG_SYNC_VERSION,
    exportedAt: 1,
    persistedValues: {
      app_future_secure_v1: {
        storage: "plain",
        value: { id: "secure", enabled: true, password: "remote-password" },
      },
    },
  });

  assert.deepEqual(persistenceState.preparedSecure, [
    { id: "secure", enabled: true, password: "local-password" },
  ]);
  assert.deepEqual(persistenceState.batches, [{ sets: [{
    key: "app_future_secure_v1",
    value: "enc:v2:prepared:1",
    browserSnapshot: { id: "secure", enabled: true, password: "" },
  }] }]);
});

test("applies future plain and secure entries but keeps local secrets", async () => {
  persistenceState.stored = {
    app_future_feature_v1: { id: "future", enabled: false, token: "enc:v2:local-token" },
  };
  persistenceState.secure = {
    app_future_secure_v1: { id: "secure", enabled: false, password: "local-password" },
  };
  persistenceState.preparedSecure = [];
  persistenceState.batches = [];
  persistenceState.runtimeSettings = [];

  const result = await configSyncPersistence.applyConfigSyncSnapshot({
    format: configSync.CONFIG_SYNC_FORMAT,
    version: configSync.CONFIG_SYNC_VERSION,
    exportedAt: 1,
    persistedValues: {
      app_future_feature_v1: {
        storage: "plain",
        value: { id: "future", enabled: true, token: "remote-token" },
      },
      app_future_secure_v1: {
        storage: "secure",
        value: { id: "secure", enabled: true, password: "remote-password" },
      },
      app_config_sync_repository_v1: {
        storage: "secure",
        value: { remoteUrl: "must-not-apply" },
      },
    },
  });

  assert.deepEqual(persistenceState.preparedSecure, [
    { id: "secure", enabled: true, password: "local-password" },
  ]);
  assert.deepEqual(persistenceState.batches, [{ sets: [
    {
      key: "app_future_feature_v1",
      value: { id: "future", enabled: true, token: "enc:v2:local-token" },
      browserSnapshot: { id: "future", enabled: true, token: "" },
    },
    {
      key: "app_future_secure_v1",
      value: "enc:v2:prepared:1",
      browserSnapshot: { id: "secure", enabled: true, password: "" },
    },
  ] }]);
  assert.deepEqual(result.skippedKeys, ["app_config_sync_repository_v1"]);
});

test("prepares every secure value before committing mixed entries", async () => {
  persistenceState.stored = {};
  persistenceState.secure = {};
  persistenceState.preparedSecure = [];
  persistenceState.batches = [];
  persistenceState.runtimeSettings = [];
  persistenceState.prepareFailure = new Error("seal failed");

  await assert.rejects(configSyncPersistence.applyConfigSyncSnapshot({
    format: configSync.CONFIG_SYNC_FORMAT,
    version: configSync.CONFIG_SYNC_VERSION,
    exportedAt: 1,
    persistedValues: {
      app_plain_v1: { storage: "plain", value: { enabled: true } },
      app_secure_v1: { storage: "secure", value: { fail: true } },
    },
  }), /seal failed/);
  assert.deepEqual(persistenceState.batches, []);
  assert.deepEqual(persistenceState.runtimeSettings, []);
  persistenceState.prepareFailure = null;
});

test("updates settings runtime only after the prepared batch succeeds", async () => {
  persistenceState.stored = {};
  persistenceState.secure = {};
  persistenceState.preparedSecure = [];
  persistenceState.batches = [];
  persistenceState.runtimeSettings = [];
  persistenceState.batchFailure = new Error("batch failed");
  const snapshot = {
    format: configSync.CONFIG_SYNC_FORMAT,
    version: configSync.CONFIG_SYNC_VERSION,
    exportedAt: 1,
    persistedValues: {
      app_settings_v2: {
        storage: "secure",
        value: { uiTheme: "dark", aiProfiles: [{ id: "a", apiKey: "remote" }] },
      },
    },
  };

  await assert.rejects(configSyncPersistence.applyConfigSyncSnapshot(snapshot), /batch failed/);
  assert.deepEqual(persistenceState.runtimeSettings, []);

  persistenceState.batchFailure = null;
  await configSyncPersistence.applyConfigSyncSnapshot(snapshot);
  assert.deepEqual(persistenceState.runtimeSettings, [{
    uiTheme: "dark",
    aiProfiles: [{ id: "a", apiKey: "" }],
  }]);
  assert.deepEqual(persistenceState.batches.at(-1).sets[0].browserSnapshot, {
    uiTheme: "dark",
    aiProfiles: [{ id: "a", apiKey: "" }],
  });
});

test("host-authored template bodies are never config-sync keys", () => {
 assert.equal(configSync.isConfigSyncStorageKey("app_file_templates_custom_v1"),false);
 assert.equal(configSync.isConfigSyncStorageKey("app_settings_v2"),true);
});

test("recently opened connection history stays on this device in both sync directions", async () => {
  persistenceState.stored = {
    app_future_feature_v1: { enabled: true },
    app_ssh_recent_v1: { "host-1": 1700000000000 },
    app_db_recent_cleared_at_v1: 1700000000000,
  };
  persistenceState.secure = {};
  const snapshot = await configSyncPersistence.createConfigSyncSnapshot();
  assert.deepEqual(Object.keys(snapshot.persistedValues), ["app_future_feature_v1"]);

  persistenceState.stored = {};
  persistenceState.batches = [];
  const result = await configSyncPersistence.applyConfigSyncSnapshot({
    format: configSync.CONFIG_SYNC_FORMAT,
    version: configSync.CONFIG_SYNC_VERSION,
    exportedAt: 1,
    persistedValues: {
      app_ssh_recent_v1: { storage: "plain", value: { "host-1": 1 } },
      app_db_recent_cleared_at_v1: { storage: "plain", value: 1 },
    },
  });
  assert.deepEqual(result.skippedKeys.sort(), ["app_db_recent_cleared_at_v1", "app_ssh_recent_v1"]);
  assert.deepEqual(persistenceState.batches.flatMap((batch) => batch.sets), []);
});

globalThis.__configPortabilityMocks = { configSyncPersistence };
const configPortability = await importTypeScriptSource(
  "../src/services/config-portability.ts",
  (source) =>
    source
      .replace(
        'import { APP_LOCK_STORAGE_KEY } from "@/domain/security/app-lock";',
        'const APP_LOCK_STORAGE_KEY = "app_lock_v1";'
      )
      .replace(
        /import \{[\s\S]*?\} from "@\/domain\/settings\/config-sync";/,
        "const { isConfigSyncStorageKey, isSecretFieldName } = globalThis.__configSyncPersistenceMocks.domain;"
      )
      .replace(
        /import \{[\s\S]*?\} from "@\/services\/config-sync-persistence";/,
        `const {
          DEVICE_LOCAL_HISTORY_STORAGE_KEYS, isKnownSecureStorageKey, sanitizePortableValue
        } = globalThis.__configPortabilityMocks.configSyncPersistence;`
      )
      .replace(
        /import \{[\s\S]*?\} from "@\/services\/persistent-store";/,
        "const { applyPersistedBatch, loadAllPersistedValues } = globalThis.__configSyncPersistenceMocks.persistent;"
      )
      .replace(
        /import \{[\s\S]*?\} from "@\/services\/secure-persistent-store";/,
        "const { loadSecurePersistedValue, prepareSecurePersistedValue } = globalThis.__configSyncPersistenceMocks.secure;"
      )
      .replace(
        /import \{[\s\S]*?\} from "@\/services\/settings-persistence";/,
        "const { SETTINGS_STORAGE_KEY, updateSettingsRuntimeSnapshot } = globalThis.__configSyncPersistenceMocks.settings; const normalizeLegacyAiProfiles = (value) => value;"
      )
      .replace(
        'import { decryptField, encryptField } from "@/services/field-crypto";',
        "const decryptField = async (value) => value; const encryptField = async (value) => value;"
      )
      .replace(
        'import { isDesktopRuntime } from "@/services/desktop-bridge";',
        "const invokeBackend = async () => undefined; const isDesktopRuntime = () => false;"
      )
      .replace(
        'import { isRetiredAiHistoryStorageKey } from "@/services/ai-conversation-persistence";',
        "const { isRetiredAiHistoryStorageKey } = globalThis.__configSyncPersistenceMocks.ai;"
      )
);

test("config export leaves recently opened connection history out of the file", async () => {
  assert.equal(configPortability.isPortableConfigKey("app_ssh_recent_v1"), false);
  assert.equal(configPortability.isPortableConfigKey("app_db_recent_cleared_at_v1"), false);
  assert.equal(configPortability.isPortableConfigKey("app_future_feature_v1"), true);

  persistenceState.stored = {
    app_future_feature_v1: { enabled: true },
    app_ssh_recent_v1: { "host-1": 1700000000000 },
    app_db_recent_cleared_at_v1: 1700000000000,
  };
  persistenceState.secure = {};
  const payload = await configPortability.buildConfigExport({ includeSecrets: false, exportedAt: "now" });
  assert.deepEqual(Object.keys(payload.values), ["app_future_feature_v1"]);
});

test("treats saved Socket.IO events as encrypted HTTP resources", () => {
  assert.equal(configSyncPersistence.isKnownSecureStorageKey("app_http_socketio_events_v1"), true);
});
