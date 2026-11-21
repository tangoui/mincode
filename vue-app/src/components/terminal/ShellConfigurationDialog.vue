<script setup lang="ts">
import { reactive } from "vue";
import { Delete, Plus } from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";
import type { LocalShellType, LocalTerminalPaneOptions } from "@/domain/terminal/types";
import { useVueI18n } from "@/i18n/messages";

interface EnvironmentEntry {
  id: string;
  key: string;
  value: string;
}

const visible = defineModel<boolean>({ required: true });
const emit = defineEmits<{
  create: [options: LocalTerminalPaneOptions];
}>();

const { t } = useVueI18n();
const shellOptions: LocalShellType[] = [
  "system",
  "zsh",
  "bash",
  "fish",
  "pwsh",
  "powershell",
  "cmd",
  "wsl",
];
const environmentNamePattern = /^[A-Za-z_][A-Za-z0-9_]*$/;

const form = reactive({
  shellType: "system" as LocalShellType,
  customPath: "",
  initialCwd: "",
  initialCommand: "",
  environment: [] as EnvironmentEntry[],
});

function nextEnvironmentId() {
  return typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
    ? crypto.randomUUID()
    : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

function addEnvironmentEntry() {
  form.environment.push({ id: nextEnvironmentId(), key: "", value: "" });
}

function removeEnvironmentEntry(id: string) {
  const index = form.environment.findIndex((entry) => entry.id === id);
  if (index >= 0) form.environment.splice(index, 1);
}

function resetForm() {
  form.shellType = "system";
  form.customPath = "";
  form.initialCwd = "";
  form.initialCommand = "";
  form.environment.splice(0);
}

function optionalValue(value: string) {
  const trimmed = value.trim();
  return trimmed || undefined;
}

function createShell() {
  const extraEnv: Record<string, string> = {};
  for (const entry of form.environment) {
    const key = entry.key.trim();
    if (!key && !entry.value) continue;
    if (!environmentNamePattern.test(key)) {
      ElMessage.warning(t("terminal.environmentNameInvalid"));
      return;
    }
    extraEnv[key] = entry.value;
  }

  emit("create", {
    shellType: form.shellType,
    customPath: optionalValue(form.customPath),
    initialCwd: optionalValue(form.initialCwd),
    initialCommand: optionalValue(form.initialCommand),
    extraEnv: Object.keys(extraEnv).length > 0 ? extraEnv : undefined,
  });
  visible.value = false;
  resetForm();
}
</script>

<template>
  <el-dialog
    append-to-body
    v-model="visible"
    :title="t('terminal.configurationTitle')"
    width="min(620px, calc(100vw - 48px))"
    destroy-on-close
    @closed="resetForm"
  >
    <el-form label-position="top" class="shell-configuration-form">
      <div class="grid grid-cols-1 gap-x-4 sm:grid-cols-2">
        <el-form-item :label="t('terminal.shellType')">
          <el-select v-model="form.shellType" class="w-full">
            <el-option
              v-for="shell in shellOptions"
              :key="shell"
              :label="shell === 'system' ? t('terminal.systemShell') : shell"
              :value="shell"
            />
          </el-select>
        </el-form-item>

        <el-form-item :label="t('terminal.customShellPath')">
          <el-input
            v-model="form.customPath"
            :placeholder="t('terminal.customShellPathPlaceholder')"
            clearable
          />
        </el-form-item>
      </div>

      <el-form-item :label="t('terminal.initialCwd')">
        <el-input
          v-model="form.initialCwd"
          :placeholder="t('terminal.initialCwdPlaceholder')"
          clearable
        />
      </el-form-item>

      <el-form-item :label="t('terminal.initialCommand')">
        <el-input
          v-model="form.initialCommand"
          type="textarea"
          :rows="2"
          resize="vertical"
          :placeholder="t('terminal.initialCommandPlaceholder')"
        />
      </el-form-item>

      <div class="mb-2 flex items-center gap-2">
        <strong class="text-xs font-medium text-[var(--el-text-color-regular)]">
          {{ t("terminal.environmentVariables") }}
        </strong>
        <span class="flex-1" />
        <el-button size="small" text @click="addEnvironmentEntry">
          <el-icon class="mr-1"><Plus /></el-icon>
          {{ t("terminal.addEnvironmentVariable") }}
        </el-button>
      </div>

      <p
        v-if="form.environment.length === 0"
        class="mb-3 rounded border border-dashed border-[var(--app-border)] px-3 py-2 text-xs text-[var(--el-text-color-secondary)]"
      >
        {{ t("terminal.environmentEmpty") }}
      </p>

      <div v-else class="mb-3 space-y-2">
        <div v-for="entry in form.environment" :key="entry.id" class="flex items-center gap-2">
          <el-input
            v-model="entry.key"
            class="min-w-0 flex-1"
            autocomplete="off"
            :placeholder="t('terminal.environmentName')"
          />
          <el-input
            v-model="entry.value"
            class="min-w-0 flex-[1.4]"
            autocomplete="off"
            :placeholder="t('terminal.environmentValue')"
          />
          <el-button
            text
            :aria-label="t('terminal.removeEnvironmentVariable')"
            @click="removeEnvironmentEntry(entry.id)"
          >
            <el-icon><Delete /></el-icon>
          </el-button>
        </div>
      </div>

      <p class="m-0 text-[11px] leading-5 text-[var(--el-text-color-secondary)]">
        {{ t("terminal.environmentSecurityNote") }}
      </p>
    </el-form>

    <template #footer>
      <el-button @click="visible = false">{{ t("common.cancel") }}</el-button>
      <el-button type="primary" @click="createShell">
        {{ t("terminal.createConfiguredShell") }}
      </el-button>
    </template>
  </el-dialog>
</template>
