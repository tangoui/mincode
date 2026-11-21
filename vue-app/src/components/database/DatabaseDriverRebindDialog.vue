<script setup lang="ts">
/**
 * Explicit (bulk) rebinding of saved connections between built-in and plugin
 * drivers (E-10). Nothing is rebound automatically: the user picks the target
 * driver and the connections. Binding to a plugin driver records the
 * credential-release grant for each connection after one protected confirm;
 * going back to the built-in driver revokes the plugin grants. The parent
 * persists the connections emitted by `apply`.
 */
import { computed, ref, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  applyDatabaseDriverRebind,
  databaseDriverBuiltinCandidates,
  databaseDriverRebindCandidates,
  databaseDriverTitle,
  parsePluginDatabaseDriverKey,
  type DatabaseDriverRebindTarget,
} from "@/domain/plugins/database-drivers";
import { databaseTypeLabels, type DatabaseConnection, type DbType } from "@/domain/database/types";
import { useVueI18n } from "@/i18n/messages";
import { loadSupportedDatabaseTypes } from "@/services/database-capabilities";
import {
  databaseDriverErrorMessage,
  databaseDriverGrantRef,
  grantDatabaseDriver,
  revokeDatabaseDriverGrant,
} from "@/services/database-driver-service";
import { isDesktopRuntime } from "@/services/desktop-bridge";
import { useDatabaseDriversStore } from "@/stores/database-drivers";

const props = defineProps<{
  modelValue: boolean;
  connections: DatabaseConnection[];
  /** Optional initial target (`plugin:<pluginId>/<driverId>` or `builtin:<dbType>`). */
  initialTarget?: string;
  /** Connections to preselect when they are candidates for the chosen target; the user can still change the selection. */
  initialConnectionIds?: string[];
}>();

const emit = defineEmits<{
  "update:modelValue": [value: boolean];
  apply: [connections: DatabaseConnection[]];
}>();

const { t, localeChain } = useVueI18n();
const driversStore = useDatabaseDriversStore();
const target = ref("");
const selectedIds = ref<string[]>([]);
const preferredIds = ref<string[]>([]);
const applying = ref(false);
const supportedTypes = ref<Set<DbType> | null>(null);

const pluginTargets = computed(() => driversStore.drivers.filter((driver) => driver.replaces));
const builtinTargets = computed(() => {
  const types = new Set<DbType>();
  for (const connection of props.connections) {
    if (connection.driver && connection.dbType !== "plugin" && (supportedTypes.value?.has(connection.dbType) ?? true)) {
      types.add(connection.dbType);
    }
  }
  return [...types].sort();
});

const resolvedTarget = computed<(DatabaseDriverRebindTarget & { dbType?: DbType }) | undefined>(() => {
  if (target.value.startsWith("builtin:")) {
    const dbType = target.value.slice("builtin:".length) as DbType;
    return builtinTargets.value.includes(dbType) ? { kind: "builtin", dbType } : undefined;
  }
  const ref = parsePluginDatabaseDriverKey(target.value);
  const driver = ref ? driversStore.driverFor(ref) : undefined;
  return driver ? { kind: "plugin", driver } : undefined;
});

const candidates = computed(() => {
  const current = resolvedTarget.value;
  if (!current) return [];
  if (current.kind === "plugin") return databaseDriverRebindCandidates(props.connections, current.driver);
  return databaseDriverBuiltinCandidates(props.connections, current.dbType ?? "plugin");
});

watch(
  () => props.modelValue,
  (visible) => {
    if (!visible) return;
    target.value = props.initialTarget ?? "";
    preferredIds.value = [...(props.initialConnectionIds ?? [])];
    selectedIds.value = preferredCandidateIds(candidates.value);
    void loadSupportedDatabaseTypes().then((types) => {
      supportedTypes.value = types;
    });
  },
  { immediate: true },
);
function preferredCandidateIds(list: readonly DatabaseConnection[]) {
  const ids = new Set(list.map((connection) => connection.id));
  return preferredIds.value.filter((id) => ids.has(id));
}
watch(candidates, (list) => {
  const ids = new Set(list.map((connection) => connection.id));
  const kept = selectedIds.value.filter((id) => ids.has(id));
  selectedIds.value = kept.length ? kept : preferredCandidateIds(list);
});

const allSelected = computed(
  () => candidates.value.length > 0 && selectedIds.value.length === candidates.value.length,
);
function toggleAll(checked: boolean) {
  selectedIds.value = checked ? candidates.value.map((connection) => connection.id) : [];
}

function connectionEndpoint(connection: DatabaseConnection) {
  if (connection.filePath && !connection.host) return connection.filePath;
  const host = connection.port ? `${connection.host}:${connection.port}` : connection.host;
  return [host, connection.username].filter(Boolean).join(", ");
}

function close() {
  emit("update:modelValue", false);
}

async function apply() {
  const current = resolvedTarget.value;
  if (!current || !selectedIds.value.length || applying.value) return;
  const changed = applyDatabaseDriverRebind(props.connections, selectedIds.value, current);
  if (!changed.length) return;
  if (current.kind === "plugin") {
    const driver = current.driver;
    try {
      await ElMessageBox.confirm(
        t("pluginContrib.databaseDrivers.grantBulkBody", {
          count: changed.length,
          plugin: `${driver.pluginName} (${[driver.pluginId, driver.pluginVersion].filter(Boolean).join(", ")})`,
          driver: databaseDriverTitle(driver, localeChain.value),
        }),
        t("pluginContrib.databaseDrivers.grantTitle"),
        {
          type: "warning",
          confirmButtonText: t("pluginContrib.databaseDrivers.grantConfirm"),
          cancelButtonText: t("common.cancel"),
          distinguishCancelAndClose: true,
          autofocus: false,
        },
      );
    } catch {
      return;
    }
  }
  applying.value = true;
  try {
    if (isDesktopRuntime()) {
      for (const connection of changed) {
        if (current.kind === "plugin" && connection.driver) {
          const ref = databaseDriverGrantRef(connection.id, connection.driver);
          await grantDatabaseDriver(ref);
          driversStore.markGranted(ref);
        } else if (current.kind === "builtin") {
          await revokeDatabaseDriverGrant({ connectionId: connection.id });
          driversStore.forgetStatuses(connection.id);
        }
      }
    }
    emit("apply", changed);
    ElMessage.success(t("pluginContrib.databaseDrivers.rebindDone", { count: changed.length }));
    close();
  } catch (error) {
    ElMessage.error(databaseDriverErrorMessage(error));
  } finally {
    applying.value = false;
  }
}
</script>

<template>
  <el-dialog
    append-to-body
    :model-value="modelValue"
    :title="t('pluginContrib.databaseDrivers.rebindTitle')"
    width="min(640px, calc(100vw - 32px))"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <p class="db-driver-rebind__body">{{ t("pluginContrib.databaseDrivers.rebindBody") }}</p>
    <el-form label-position="top">
      <el-form-item :label="t('pluginContrib.databaseDrivers.rebindTarget')">
        <el-select
          v-model="target"
          class="db-driver-rebind__select"
          :placeholder="t('pluginContrib.databaseDrivers.rebindTargetPlaceholder')"
        >
          <el-option-group
            v-if="pluginTargets.length"
            :label="t('pluginContrib.databaseDrivers.group')"
          >
            <el-option
              v-for="driver in pluginTargets"
              :key="driver.key"
              :value="driver.key"
              :label="
                t('pluginContrib.databaseDrivers.rebindPluginOption', {
                  title: databaseDriverTitle(driver, localeChain),
                  plugin: driver.pluginName,
                  type: databaseTypeLabels[driver.dbType],
                })
              "
            />
          </el-option-group>
          <el-option-group
            v-if="builtinTargets.length"
            :label="t('pluginContrib.databaseDrivers.useBuiltin')"
          >
            <el-option
              v-for="dbType in builtinTargets"
              :key="dbType"
              :value="`builtin:${dbType}`"
              :label="
                t('pluginContrib.databaseDrivers.builtinOption', {
                  type: databaseTypeLabels[dbType],
                })
              "
            />
          </el-option-group>
        </el-select>
      </el-form-item>
    </el-form>
    <template v-if="resolvedTarget">
      <el-empty
        v-if="!candidates.length"
        :image-size="56"
        :description="t('pluginContrib.databaseDrivers.rebindEmpty')"
      />
      <div v-else class="db-driver-rebind__list">
        <el-checkbox
          :model-value="allSelected"
          :indeterminate="selectedIds.length > 0 && !allSelected"
          @update:model-value="toggleAll($event === true)"
        >
          {{ t("pluginContrib.databaseDrivers.rebindSelectAll", { count: candidates.length }) }}
        </el-checkbox>
        <el-checkbox-group v-model="selectedIds" class="db-driver-rebind__items">
          <el-checkbox
            v-for="connection in candidates"
            :key="connection.id"
            :value="connection.id"
          >
            <span class="db-driver-rebind__name">{{ connection.name }}</span>
            <small class="db-driver-rebind__endpoint">{{ connectionEndpoint(connection) }}</small>
          </el-checkbox>
        </el-checkbox-group>
      </div>
    </template>
    <template #footer>
      <el-button @click="close">{{ t("common.cancel") }}</el-button>
      <el-button
        type="primary"
        :disabled="!resolvedTarget || !selectedIds.length"
        :loading="applying"
        @click="apply"
      >
        {{ t("pluginContrib.databaseDrivers.rebindApply", { count: selectedIds.length }) }}
      </el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.db-driver-rebind__body {
  margin: 0 0 12px;
  color: var(--el-text-color-secondary);
  font-size: 12px;
  line-height: 1.5;
}
.db-driver-rebind__select {
  width: 100%;
}
.db-driver-rebind__list {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.db-driver-rebind__items {
  display: flex;
  max-height: 280px;
  flex-direction: column;
  align-items: stretch;
  gap: 2px;
  overflow-y: auto;
  padding: 6px 8px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
}
.db-driver-rebind__items :deep(.el-checkbox) {
  height: auto;
  min-height: 28px;
  margin-right: 0;
}
.db-driver-rebind__name {
  margin-right: 8px;
}
.db-driver-rebind__endpoint {
  color: var(--el-text-color-secondary);
}
</style>
