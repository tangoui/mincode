<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { FolderOpened, Refresh } from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";
import {
  findOrphanedSshConfigProfiles,
  mergeSshConfigProfile,
  sshConfigSourceKey,
  type SshConfigDiscoveredProfile,
} from "@/domain/ssh/types";
import { useVueI18n } from "@/i18n/messages";
import {
  discoverSshConfig,
  type SshConfigDiscoveryResult,
} from "@/services/ssh-security-service";
import {
  chooseNativeFileOrDirectory,
  isDesktopRuntime,
} from "@/services/desktop-bridge";
import { useSshConnectionsStore } from "@/stores/ssh-connections";

const props = defineProps<{ modelValue: boolean }>();
const emit = defineEmits<{ "update:modelValue": [value: boolean] }>();
const { t } = useVueI18n();
const connections = useSshConnectionsStore();
const loading = ref(false);
const syncing = ref(false);
const result = ref<SshConfigDiscoveryResult>();
const selected = ref<string[]>([]);

const profiles = computed(() => result.value?.profiles ?? []);
const existingKeys = computed(
  () =>
    new Set(
      connections.profiles.flatMap((profile) =>
        profile.sshConfigSource
          ? [sshConfigSourceKey(profile.sshConfigSource.sourcePath, profile.sshConfigSource.alias)]
          : []
      )
    )
);
const orphanedProfiles = computed(() =>
  result.value
    ? findOrphanedSshConfigProfiles(
        connections.profiles,
        result.value.profiles,
        result.value.path
      )
    : []
);
const multiHopCount = computed(
  () => profiles.value.filter((profile) => profile.proxyJump?.includes(",")).length
);

async function load(path?: string) {
  loading.value = true;
  try {
    result.value = await discoverSshConfig(path);
    selected.value = profiles.value.map((profile) =>
      sshConfigSourceKey(profile.sourcePath, profile.alias)
    );
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : t("ssh.config.loadFailed"));
  } finally {
    loading.value = false;
  }
}

async function chooseSource() {
  if (!isDesktopRuntime()) return;
  const path = await chooseNativeFileOrDirectory(t("ssh.config.chooseSource"));
  if (path) await load(path);
}

function existingFor(profile: SshConfigDiscoveredProfile) {
  const key = sshConfigSourceKey(profile.sourcePath, profile.alias);
  return connections.profiles.find((candidate) => {
    const source = candidate.sshConfigSource;
    return source && sshConfigSourceKey(source.sourcePath, source.alias) === key;
  });
}

async function sync() {
  if (!result.value) return;
  syncing.value = true;
  let count = 0;
  try {
    for (const item of result.value.profiles) {
      if (!selected.value.includes(sshConfigSourceKey(item.sourcePath, item.alias))) continue;
      connections.saveProfile(
        mergeSshConfigProfile(
          item,
          result.value.profiles,
          existingFor(item),
          result.value.path
        )
      );
      count++;
    }
    await connections.persistProfiles();
    ElMessage.success(t("ssh.config.synced").replace("{count}", String(count)));
    emit("update:modelValue", false);
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : t("ssh.config.syncFailed"));
  } finally {
    syncing.value = false;
  }
}

watch(
  () => props.modelValue,
  (visible) => {
    if (visible) void load(result.value?.path);
  },
  { immediate: true }
);
</script>

<template>
  <el-dialog
    append-to-body
    class="ssh-config-dialog"
    :model-value="modelValue"
    width="min(820px, calc(100vw - 40px))"
    align-center
    destroy-on-close
    @update:model-value="emit('update:modelValue', $event)"
  >
    <template #header>
      <strong>{{ t("ssh.config.title") }}</strong>
    </template>
    <div v-loading="loading" class="ssh-config-dialog__body">
      <div class="ssh-config-dialog__toolbar">
        <code :title="result?.path">{{ result?.path || t("ssh.config.defaultSource") }}</code>
        <div class="ssh-config-dialog__actions">
          <el-button :icon="FolderOpened" @click="chooseSource">
            {{ t("ssh.config.chooseSource") }}
          </el-button>
          <el-button :icon="Refresh" :disabled="!result" @click="load(result?.path)">
            {{ t("common.refresh") }}
          </el-button>
        </div>
      </div>

      <el-alert
        v-if="result?.warnings.length"
        class="ssh-config-dialog__alert"
        type="warning"
        :closable="false"
        :title="t('ssh.config.warningCount').replace('{count}', String(result.warnings.length))"
      />
      <el-alert
        v-if="multiHopCount"
        class="ssh-config-dialog__alert"
        type="info"
        :closable="false"
        :title="t('ssh.config.multiHopNotice').replace('{count}', String(multiHopCount))"
      />
      <el-alert
        v-if="orphanedProfiles.length"
        class="ssh-config-dialog__alert"
        type="warning"
        :closable="false"
        :title="t('ssh.config.orphaned').replace('{names}', orphanedProfiles.map((profile) => profile.name).join(', '))"
      />

      <el-checkbox-group v-model="selected" class="ssh-config-sync-list">
        <el-checkbox
          v-for="profile in profiles"
          :key="sshConfigSourceKey(profile.sourcePath, profile.alias)"
          :value="sshConfigSourceKey(profile.sourcePath, profile.alias)"
          border
        >
          <span class="ssh-config-sync-list__content">
            <span class="ssh-config-sync-list__heading">
              <strong>{{ profile.alias }}</strong>
              <el-tag
                size="small"
                :type="existingKeys.has(sshConfigSourceKey(profile.sourcePath, profile.alias)) ? 'info' : 'success'"
                effect="plain"
              >
                {{
                  existingKeys.has(sshConfigSourceKey(profile.sourcePath, profile.alias))
                    ? t("ssh.config.existing")
                    : t("ssh.config.new")
                }}
              </el-tag>
            </span>
            <small>{{ profile.username }}@{{ profile.host }}:{{ profile.port }}</small>
            <small v-if="profile.proxyJump">
              {{ t("ssh.config.via") }} {{ profile.proxyJump }}
            </small>
          </span>
        </el-checkbox>
      </el-checkbox-group>
      <el-empty
        v-if="!loading && !profiles.length"
        :description="t('ssh.config.empty')"
        :image-size="64"
      />
    </div>
    <template #footer>
      <el-button @click="emit('update:modelValue', false)">{{ t("common.cancel") }}</el-button>
      <el-button
        type="primary"
        :loading="syncing"
        :disabled="!selected.length"
        @click="sync"
      >
        {{ t("ssh.config.syncSelected") }}
      </el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.ssh-config-dialog__body {
  min-height: 250px;
}

.ssh-config-dialog__toolbar {
  display: flex;
  min-width: 0;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 10px;
}

.ssh-config-dialog__toolbar > code {
  min-width: 0;
  overflow: hidden;
  color: var(--app-text-muted);
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ssh-config-dialog__actions {
  display: flex;
  flex: none;
  gap: 8px;
}

.ssh-config-dialog__alert {
  margin-bottom: 8px;
}

.ssh-config-sync-list {
  display: grid;
  max-height: 430px;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
  overflow: auto;
}

.ssh-config-sync-list > .el-checkbox {
  box-sizing: border-box;
  width: 100%;
  height: auto;
  min-height: 68px;
  margin: 0;
  align-items: flex-start;
  padding: 10px;
}

.ssh-config-sync-list__content {
  display: flex;
  min-width: 0;
  flex: 1;
  flex-direction: column;
  gap: 3px;
}

.ssh-config-sync-list__heading {
  display: flex;
  min-width: 0;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.ssh-config-sync-list__heading strong,
.ssh-config-sync-list__content small {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ssh-config-sync-list__content small {
  color: var(--app-text-muted);
  font-size: 11px;
}

@media (max-width: 700px) {
  .ssh-config-dialog__toolbar {
    align-items: stretch;
    flex-direction: column;
  }

  .ssh-config-dialog__actions {
    justify-content: flex-end;
  }

  .ssh-config-sync-list {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
