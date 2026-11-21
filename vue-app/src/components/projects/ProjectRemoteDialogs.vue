<script setup lang="ts">
import { ForkSpoon } from "@element-plus/icons-vue";
import type { SshConnectionProfile } from "@/domain/ssh/types";
import { useVueI18n } from "@/i18n/messages";

interface GitBranchEntry {
  name: string;
  is_current: boolean;
}

const props = defineProps<{
  branches?: GitBranchEntry[];
  branchLoading: boolean;
  profiles: SshConnectionProfile[];
  deploymentScopeLabel: string;
  deploying: boolean;
}>();
const emit = defineEmits<{
  compare: [gitRef: string];
  openRemote: [];
  saveDeployment: [];
  remoteClosed: [];
  deploymentClosed: [];
}>();
const branchVisible = defineModel<boolean>("branchVisible", { required: true });
const remoteVisible = defineModel<boolean>("remoteVisible", { required: true });
const deploymentVisible = defineModel<boolean>("deploymentVisible", { required: true });
const remoteProfileId = defineModel<string>("remoteProfileId", { required: true });
const deploymentProfileId = defineModel<string>("deploymentProfileId", { required: true });
const deploymentRemoteRoot = defineModel<string>("deploymentRemoteRoot", { required: true });
const deploymentExcludes = defineModel<string>("deploymentExcludes", { required: true });
const { t } = useVueI18n();
</script>

<template>
  <el-dialog v-model="branchVisible" :title="t('project.git.compareWithRef')" width="420px" class="project-deployment-dialog" append-to-body>
    <div v-if="branchLoading" class="project-dialog-state">{{ t("project.loadingFile") }}</div>
    <el-empty v-else-if="!props.branches?.length" :image-size="56" :description="t('project.git.branchesFailed')" />
    <el-scrollbar v-else class="project-branch-picker">
      <el-button v-for="branch in props.branches" :key="branch.name" text class="project-branch-picker__item" @click="emit('compare', branch.name)">
        <el-icon><ForkSpoon /></el-icon>{{ branch.name }}
        <small v-if="branch.is_current">{{ t("project.git.currentBranch") }}</small>
      </el-button>
    </el-scrollbar>
  </el-dialog>

  <el-dialog v-model="remoteVisible" :title="t('project.remote.openServer')" width="420px" class="project-deployment-dialog" @closed="emit('remoteClosed')" append-to-body>
    <el-form label-position="top">
      <el-form-item :label="t('project.remote.connection')">
        <el-select v-model="remoteProfileId" :placeholder="t('project.remote.connectionPlaceholder')" filterable>
          <el-option v-for="profile in profiles" :key="profile.id" :value="profile.id" :label="`${profile.name} · ${profile.username}@${profile.host}:${profile.port}`" />
        </el-select>
      </el-form-item>
    </el-form>
    <el-empty v-if="!profiles.length" :image-size="56" :description="t('project.remote.noConnection')" />
    <template #footer>
      <el-button @click="remoteVisible = false">{{ t("common.cancel") }}</el-button>
      <el-button type="primary" :disabled="!remoteProfileId" @click="emit('openRemote')">{{ t("project.remote.open") }}</el-button>
    </template>
  </el-dialog>

  <el-dialog v-model="deploymentVisible" :title="t('project.remote.configure')" width="520px" class="project-deployment-dialog" @closed="emit('deploymentClosed')" append-to-body>
    <p class="project-deployment-dialog__scope">{{ t("project.remote.scope", { path: deploymentScopeLabel }) }}</p>
    <el-form label-position="top">
      <el-form-item :label="t('project.remote.connection')">
        <el-select v-model="deploymentProfileId" :placeholder="t('project.remote.connectionPlaceholder')" filterable>
          <el-option v-for="profile in profiles" :key="profile.id" :value="profile.id" :label="`${profile.name} · ${profile.username}@${profile.host}:${profile.port}`" />
        </el-select>
      </el-form-item>
      <el-form-item :label="t('project.remote.remoteRoot')"><el-input v-model="deploymentRemoteRoot" :placeholder="t('project.remote.remoteRootPlaceholder')" /></el-form-item>
      <el-form-item :label="t('project.remote.excludes')"><el-input v-model="deploymentExcludes" type="textarea" :rows="5" :placeholder="t('project.remote.excludesPlaceholder')" /></el-form-item>
    </el-form>
    <el-empty v-if="!profiles.length" :image-size="56" :description="t('project.remote.noConnection')" />
    <template #footer>
      <el-button @click="deploymentVisible = false">{{ t("common.cancel") }}</el-button>
      <el-button type="primary" :loading="deploying" :disabled="!profiles.length" @click="emit('saveDeployment')">{{ t("common.save") }}</el-button>
    </template>
  </el-dialog>
</template>
