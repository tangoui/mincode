<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { storeToRefs } from "pinia";
import { ElMessage } from "element-plus";
import {
  Check,
  Connection,
  DataBoard,
  Download,
  FolderOpened,
  Lock,
  Upload,
} from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";
import { useWorkspaceStateScope } from "@/composables/use-workspace-state-scope";
import {
  buildEnvironmentPackPreview,
  decodeEnvironmentPack,
  encodeEnvironmentPack,
  type EnvironmentPackData,
  type EnvironmentPackPreview,
} from "@/domain/workspace/environment-pack";
import type { DatabaseConnection } from "@/domain/database/types";
import type { SshRuntimeCredentials } from "@/domain/ssh/types";
import { restoreSshProfile, useSshConnectionsStore } from "@/stores/ssh-connections";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import { useHttpResourcesStore } from "@/stores/http-resources";
import {
  chooseEnvironmentPackFile,
  saveEnvironmentPackFile,
} from "@/services/environment-pack-service";
import { isDesktopRuntime } from "@/services/desktop-bridge";

const props = defineProps<{ modelValue: boolean }>();
const emit = defineEmits<{ "update:modelValue": [value: boolean] }>();
const { t } = useVueI18n();
const sshStore = useSshConnectionsStore();
const databaseStore = useDatabaseWorkspaceStore();
const httpResourcesStore = useHttpResourcesStore();
const { collections: httpCollections } = storeToRefs(httpResourcesStore);
const workspaceStateScope = useWorkspaceStateScope();

const activeTab = ref<"export" | "import">("export");
const includeSsh = ref(true);
const includeDatabase = ref(true);
const includeHttp = ref(true);
const exportPassword = ref("");
const exportConfirm = ref("");
const exporting = ref(false);
const importFilename = ref("");
const importContent = ref("");
const importPassword = ref("");
const parsing = ref(false);
const importing = ref(false);
const parsed = ref<EnvironmentPackData>();
const preview = ref<EnvironmentPackPreview>();
const exportCounts = computed(() => ({
  ssh: sshStore.profiles.length,
  database: databaseStore.connections.length,
  http: httpCollections.value.length,
}));
const passwordMismatch = computed(
  () => Boolean(exportConfirm.value) && exportPassword.value !== exportConfirm.value
);
const canExport = computed(
  () =>
    [includeSsh.value, includeDatabase.value, includeHttp.value].some(
      Boolean
    ) &&
    exportPassword.value.length >= 8 &&
    exportPassword.value === exportConfirm.value
);
const desktopRuntime = computed(() => isDesktopRuntime());
const runtimeHintKey = computed(() =>
  desktopRuntime.value ? "settings.data.envRuntimeDesktop" : "settings.data.envRuntimeBrowser"
);

function cloneValue<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

function errorText(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

async function hydrateSources() {
  await workspaceStateScope.hydrate();
  await Promise.all([
    sshStore.hydrated ? Promise.resolve() : sshStore.hydrate(),
    databaseStore.initialize(),
    httpResourcesStore.hydrate(),
  ]);
}

watch(
  () => props.modelValue,
  (open) => {
    if (open) void hydrateSources().catch((error) => ElMessage.error(errorText(error)));
  },
  { immediate: true }
);

function close() {
  emit("update:modelValue", false);
}

function selectedSshCredentials() {
  if (!includeSsh.value) return {};
  return Object.fromEntries(
    sshStore.profiles.flatMap((profile) => {
      const credentials = sshStore.credentialsFor(profile.id);
      return credentials && Object.keys(credentials).length ? [[profile.id, credentials]] : [];
    })
  ) as Record<string, SshRuntimeCredentials>;
}

function selectedItems(): EnvironmentPackData["items"] {
  return {
    sshConnections: includeSsh.value ? sshStore.profiles.map((profile) => cloneValue(profile)) : [],
    sshCredentials: selectedSshCredentials(),
    dbConnections: includeDatabase.value
      ? databaseStore.connections.map((connection) => cloneValue(connection))
      : [],
    httpCollections: includeHttp.value ? cloneValue(httpCollections.value) : [],
    // 旧字段只维持环境包格式兼容；环境随顶级集合导出。
    httpEnvironments: { environments: [], activeEnvId: "" },
    localProjects: [],
  };
}

async function exportPack() {
  if (!canExport.value) {
    if (exportPassword.value.length < 8) ElMessage.warning(t("settings.data.envPasswordLength"));
    else if (passwordMismatch.value) ElMessage.warning(t("settings.data.envPasswordMismatch"));
    else ElMessage.warning(t("settings.data.envSelectOne"));
    return;
  }
  exporting.value = true;
  try {
    await hydrateSources();
    const data: EnvironmentPackData = {
      version: 2,
      exportedAt: Date.now(),
      appName: "MinCode",
      items: selectedItems(),
    };
    const content = await encodeEnvironmentPack(data, exportPassword.value);
    const filename = `mincode-environment-${new Date().toISOString().slice(0, 10)}.mincode`;
    if (await saveEnvironmentPackFile(t("settings.data.envSaveTitle"), filename, content)) {
      ElMessage.success(t("settings.data.envExported"));
      exportPassword.value = "";
      exportConfirm.value = "";
    }
  } catch (error) {
    ElMessage.error(t("settings.data.envExportFailed", { message: errorText(error) }));
  } finally {
    exporting.value = false;
  }
}

async function selectImportFile() {
  try {
    const selected = await chooseEnvironmentPackFile(t("settings.data.envChooseTitle"));
    if (!selected) return;
    importFilename.value = selected.name;
    importContent.value = selected.content;
    importPassword.value = "";
    parsed.value = undefined;
    preview.value = undefined;
  } catch (error) {
    ElMessage.error(t("settings.data.envReadFailed", { message: errorText(error) }));
  }
}

function resetParsedImport() {
  parsed.value = undefined;
  preview.value = undefined;
}

async function parseImport() {
  if (!importContent.value) return ElMessage.warning(t("settings.data.envChooseFirst"));
  parsing.value = true;
  try {
    await hydrateSources();
    const data = await decodeEnvironmentPack(importContent.value, importPassword.value);
    const sshConnections = data.items.sshConnections.map(restoreSshProfile);
    if (sshConnections.some((profile) => profile === null)) {
      throw new Error(t("settings.data.envInvalidSsh"));
    }
    data.items.sshConnections = sshConnections.filter(
      (profile): profile is NonNullable<typeof profile> => profile !== null
    );
    parsed.value = data;
    preview.value = buildEnvironmentPackPreview(data, {
      sshIds: new Set(sshStore.profiles.map((profile) => profile.id)),
      databaseIds: new Set(databaseStore.connections.map((connection) => connection.id)),
      httpIds: new Set(httpCollections.value.map((collection) => collection.id)),
      projectRoots: new Set(),
    });
  } catch (error) {
    parsed.value = undefined;
    preview.value = undefined;
    ElMessage.error(t("settings.data.envParseFailed", { message: errorText(error) }));
  } finally {
    parsing.value = false;
  }
}

async function confirmImport() {
  if (!parsed.value) return;
  if (!desktopRuntime.value) return ElMessage.warning(t("common.desktopRequired"));
  importing.value = true;
  // 记录本次新增项；后续任一存储提交失败时按 ID 补偿删除，避免报告失败但留下半个环境包。
  const addedSshIds: string[] = [];
  const addedDatabaseIds: string[] = [];
  const addedHttpCollectionIds: string[] = [];
  const addedHttpEnvironmentIds: string[] = [];
  try {
    const data = parsed.value;
    let sshAdded = 0;
    const sshIds = new Set(sshStore.profiles.map((profile) => profile.id));
    for (const profile of data.items.sshConnections) {
      if (sshIds.has(profile.id)) continue;
      sshStore.saveProfile(profile);
      addedSshIds.push(profile.id);
      const credentials = data.items.sshCredentials?.[profile.id];
      if (credentials) await sshStore.saveCredentials(profile.id, credentials);
      sshIds.add(profile.id);
      sshAdded += 1;
    }
    if (addedSshIds.length) await sshStore.persistProfiles();

    let databaseAdded = 0;
    const databaseIds = new Set(databaseStore.connections.map((connection) => connection.id));
    for (const connection of data.items.dbConnections as DatabaseConnection[]) {
      if (databaseIds.has(connection.id)) continue;
      addedDatabaseIds.push(connection.id);
      await databaseStore.saveConnection(connection);
      databaseIds.add(connection.id);
      databaseAdded += 1;
    }

    const existingHttpCollectionIds = new Set(httpCollections.value.map((item) => item.id));
    addedHttpCollectionIds.push(
      ...data.items.httpCollections
        .filter((collection) => !existingHttpCollectionIds.has(collection.id))
        .map((collection) => collection.id)
    );
    const httpAdded = httpResourcesStore.mergeResources({
      version: 1,
      collections: data.items.httpCollections,
      environments: [],
    });
    await httpResourcesStore.flushPersistence();

    const total =
      sshAdded +
      databaseAdded +
      httpAdded.collections +
      httpAdded.environments;
    ElMessage.success(t("settings.data.envImported", { count: total }));
    importFilename.value = "";
    importContent.value = "";
    importPassword.value = "";
    parsed.value = undefined;
    preview.value = undefined;
    await hydrateSources();
  } catch (error) {
    // 各 store 的导入都只追加不覆盖；补偿步骤全部尝试执行，单项失败不阻断其他资源回滚。
    const rollbackResults = await Promise.allSettled([
      (async () => {
        for (const id of addedSshIds) sshStore.removeProfile(id);
        const results = await Promise.allSettled([
          ...addedSshIds.map((id) => sshStore.removeCredentials(id)),
          ...(addedSshIds.length ? [sshStore.persistProfiles()] : []),
        ]);
        const failure = results.find(
          (result): result is PromiseRejectedResult => result.status === "rejected"
        );
        if (failure) throw failure.reason;
      })(),
      (async () => {
        const results = await Promise.allSettled(
          addedDatabaseIds.map((id) => databaseStore.deleteConnection(id))
        );
        const failure = results.find(
          (result): result is PromiseRejectedResult => result.status === "rejected"
        );
        if (failure) throw failure.reason;
      })(),
      (async () => {
        httpResourcesStore.removeResources({
          collectionIds: addedHttpCollectionIds,
          environmentIds: addedHttpEnvironmentIds,
        });
        await httpResourcesStore.flushPersistence();
      })(),
    ]);
    const rollbackFailure = rollbackResults.find(
      (result): result is PromiseRejectedResult => result.status === "rejected"
    );
    if (rollbackFailure) {
      ElMessage.error(
        t("settings.data.envImportRollbackFailed", { message: errorText(rollbackFailure.reason) })
      );
      return;
    }
    ElMessage.error(t("settings.data.envImportFailed", { message: errorText(error) }));
  } finally {
    importing.value = false;
  }
}

const previewRows = computed(() => {
  if (!preview.value) return [];
  return [
    { key: "ssh", label: t("settings.data.envSsh"), icon: Connection, count: preview.value.ssh },
    {
      key: "database",
      label: t("settings.data.envDatabase"),
      icon: DataBoard,
      count: preview.value.database,
    },
    { key: "http", label: t("settings.data.envHttp"), icon: Download, count: preview.value.http },
    {
      key: "projects",
      label: t("settings.data.envProjects"),
      icon: FolderOpened,
      count: preview.value.projects,
    },
  ];
});
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    class="environment-pack-dialog"
    width="min(760px, calc(100vw - 40px))"
    append-to-body
    :show-close="false"
    @close="close"
  >
    <template #header>
      <div class="environment-pack-dialog__header">
        <div>
          <strong>{{ t("settings.data.envPack") }}</strong>
          <span>{{ t("settings.data.envSubtitle") }}</span>
        </div>
        <el-button text circle :aria-label="t('common.close')" @click="close">×</el-button>
      </div>
    </template>

    <div class="environment-pack-dialog__runtime-note">
      {{ t(runtimeHintKey) }}
    </div>

    <el-tabs v-model="activeTab" class="environment-pack-dialog__tabs">
      <el-tab-pane name="export">
        <template #label
          ><el-icon><Download /></el-icon>{{ t("settings.data.envExportTab") }}</template
        >
        <div class="environment-pack-dialog__pane">
          <div class="environment-pack-dialog__selection">
            <div class="environment-pack-dialog__selection-item" :class="{ 'is-selected': includeSsh }">
              <el-checkbox v-model="includeSsh" />
              <el-icon><Connection /></el-icon>
              <span
                ><strong>{{ t("settings.data.envSsh") }}</strong
                ><small>{{ exportCounts.ssh }}</small></span
              >
            </div>
            <div class="environment-pack-dialog__selection-item" :class="{ 'is-selected': includeDatabase }">
              <el-checkbox v-model="includeDatabase" />
              <el-icon><DataBoard /></el-icon>
              <span
                ><strong>{{ t("settings.data.envDatabase") }}</strong
                ><small>{{ exportCounts.database }}</small></span
              >
            </div>
            <div class="environment-pack-dialog__selection-item" :class="{ 'is-selected': includeHttp }">
              <el-checkbox v-model="includeHttp" />
              <el-icon><Download /></el-icon>
              <span
                ><strong>{{ t("settings.data.envHttp") }}</strong
                ><small>{{ exportCounts.http }}</small></span
              >
            </div>
          </div>

          <div class="environment-pack-dialog__passwords">
            <div class="environment-pack-dialog__password-field">
              <span>{{ t("settings.data.envPassword") }}</span>
              <el-input
                v-model="exportPassword"
                type="password"
                show-password
                autocomplete="new-password"
              />
            </div>
            <div class="environment-pack-dialog__password-field">
              <span>{{ t("settings.data.envPasswordConfirm") }}</span>
              <el-input
                v-model="exportConfirm"
                type="password"
                show-password
                autocomplete="new-password"
                :class="{ 'is-error': passwordMismatch }"
              />
            </div>
          </div>
          <p class="environment-pack-dialog__hint">
            <el-icon><Lock /></el-icon>{{ t("settings.data.envSecurityHint") }}
          </p>
          <el-button type="primary" :loading="exporting" :disabled="!canExport" @click="exportPack">
            {{ t("settings.data.envGenerate") }}
          </el-button>
        </div>
      </el-tab-pane>

      <el-tab-pane name="import">
        <template #label
          ><el-icon><Upload /></el-icon>{{ t("settings.data.envImportTab") }}</template
        >
        <div class="environment-pack-dialog__pane">
          <el-button
            text
            native-type="button"
            class="environment-pack-dialog__file"
            @click="selectImportFile"
          >
            <el-icon><Upload /></el-icon>
            <span>
              <strong>{{ importFilename || t("settings.data.envChooseFile") }}</strong>
              <small>{{
                importFilename
                  ? t("settings.data.envChooseAgain")
                  : t("settings.data.envChooseHint")
              }}</small>
            </span>
          </el-button>
          <div v-if="importFilename" class="environment-pack-dialog__import-password">
            <span>{{ t("settings.data.envImportPassword") }}</span>
            <el-input
              v-model="importPassword"
              type="password"
              show-password
              autocomplete="current-password"
              @input="resetParsedImport"
            />
          </div>
          <el-button v-if="importFilename && !preview" :loading="parsing" @click="parseImport">
            {{ t("settings.data.envParse") }}
          </el-button>

          <div v-if="preview" class="environment-pack-dialog__preview">
            <div class="environment-pack-dialog__preview-head">
              <span>{{ t("settings.data.envCategory") }}</span>
              <span>{{ t("settings.data.envTotal") }}</span>
              <span>{{ t("settings.data.envNew") }}</span>
              <span>{{ t("settings.data.envExisting") }}</span>
            </div>
            <div
              v-for="row in previewRows"
              :key="row.key"
              class="environment-pack-dialog__preview-row"
            >
              <span
                ><el-icon><component :is="row.icon" /></el-icon>{{ row.label }}</span
              >
              <span>{{ row.count.total }}</span>
              <span class="is-new">{{ row.count.newCount }}</span>
              <span>{{ row.count.skipCount }}</span>
            </div>
            <el-button
              type="primary"
              :icon="Check"
              :loading="importing"
              :disabled="!desktopRuntime"
              @click="confirmImport"
            >
              {{ t("settings.data.envConfirmImport") }}
            </el-button>
          </div>
        </div>
      </el-tab-pane>
    </el-tabs>
  </el-dialog>
</template>
