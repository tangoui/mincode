<script setup lang="ts">
// Host dialog for the backup password (backup request kind backupPassword).
// Owned by the host, outside any plugin: the password goes straight to the Go
// host and is never shown to the plugin. "set" asks twice and needs at least
// BACKUP_PASSWORD_MIN_LENGTH characters; "open" asks once and, when a
// password already exists, only replaces it if the user opts in. The inputs
// are cleared as soon as the dialog closes.
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from "vue";
import { ElAlert, ElButton, ElCheckbox, ElDialog, ElInput } from "element-plus";
import {
  BACKUP_PASSWORD_MIN_LENGTH,
  backupPasswordIssue,
  backupPasswordRememberDefault,
  backupPasswordRememberHintKey,
  backupPasswordRememberLabelKey,
  type BackupPasswordRequest,
} from "@/domain/backup/requests";
import { visibleReviewText } from "@/domain/plugin-ui/ai-config";
import { useVueI18n } from "@/i18n/messages";

const props = defineProps<{
  request: BackupPasswordRequest;
}>();
const emit = defineEmits<{
  submit: [answer: { password: string; remember: boolean }];
  cancel: [];
}>();

const { t } = useVueI18n();

const password = ref("");
const repeat = ref("");
const remember = ref(backupPasswordRememberDefault(props.request));
// Errors appear once the user tried to continue, not while typing.
const attempted = ref(false);
const passwordInput = ref<{ focus: () => void }>();

const setting = computed(() => props.request.mode === "set");
const issue = computed(() => backupPasswordIssue(props.request.mode, password.value, repeat.value));
const issueText = computed(() => {
  if (!attempted.value || !issue.value) return "";
  if (issue.value === "tooShort") return t("pluginBackup.password.tooShort", { min: BACKUP_PASSWORD_MIN_LENGTH });
  return t(`pluginBackup.password.${issue.value}`);
});
const title = computed(() => t(setting.value ? "pluginBackup.password.titleSet" : "pluginBackup.password.titleOpen"));
// The plugin's display name comes from its manifest: make hidden characters visible.
const origin = computed(() => t("pluginBackup.from", { plugin: visibleReviewText(props.request.pluginName) }));

const visible = computed({
  get: () => true,
  set: (next: boolean) => {
    if (!next) cancel();
  },
});

function clearInputs() {
  password.value = "";
  repeat.value = "";
}
onBeforeUnmount(clearInputs);
onMounted(() => void nextTick(() => passwordInput.value?.focus()));

function submit() {
  attempted.value = true;
  if (issue.value) return;
  const answer = { password: password.value, remember: remember.value };
  clearInputs();
  emit("submit", answer);
}

function cancel() {
  clearInputs();
  emit("cancel");
}

/** Enter that confirms an input-method composition is not a submit. */
function onEnter(event: Event | KeyboardEvent) {
  if ("isComposing" in event && (event.isComposing || event.keyCode === 229)) return;
  event.preventDefault();
  submit();
}
</script>

<template>
  <ElDialog
    v-model="visible"
    :title="title"
    width="min(460px, calc(100vw - 32px))"
    align-center
    append-to-body
    :close-on-click-modal="false"
    class="app-plugin-backup-password"
  >
    <div class="app-plugin-backup-password__body">
      <p class="app-plugin-backup-password__origin">{{ origin }}</p>
      <p class="app-plugin-backup-password__intro">
        {{ t(setting ? "pluginBackup.password.setIntro" : "pluginBackup.password.openIntro") }}
      </p>
      <ElAlert
        v-if="setting"
        type="warning"
        :closable="false"
        show-icon
        :title="t('pluginBackup.password.forgotWarning')"
      />
      <p v-if="setting" class="app-plugin-backup-password__hint">{{ t("pluginBackup.password.managerHint") }}</p>
      <label class="app-plugin-backup-password__field">
        <span>{{ t("pluginBackup.password.password") }}</span>
        <ElInput
          ref="passwordInput"
          v-model="password"
          type="password"
          show-password
          autocomplete="new-password"
          :aria-label="t('pluginBackup.password.password')"
          :placeholder="setting ? t('pluginBackup.password.placeholder', { min: BACKUP_PASSWORD_MIN_LENGTH }) : ''"
          @keydown.enter="onEnter"
        />
      </label>
      <label v-if="setting" class="app-plugin-backup-password__field">
        <span>{{ t("pluginBackup.password.repeat") }}</span>
        <ElInput
          v-model="repeat"
          type="password"
          show-password
          autocomplete="new-password"
          :aria-label="t('pluginBackup.password.repeat')"
          @keydown.enter="onEnter"
        />
      </label>
      <p v-if="issueText" class="app-plugin-backup-password__error" role="alert">{{ issueText }}</p>
      <ElCheckbox v-model="remember">{{ t(backupPasswordRememberLabelKey(request)) }}</ElCheckbox>
      <p class="app-plugin-backup-password__hint">
        {{ t(backupPasswordRememberHintKey(request, remember)) }}
      </p>
    </div>
    <template #footer>
      <ElButton @click="cancel">{{ t("common.cancel") }}</ElButton>
      <ElButton type="primary" @click="submit">{{ t("pluginBackup.password.submit") }}</ElButton>
    </template>
  </ElDialog>
</template>

<style scoped>
.app-plugin-backup-password__body {
  display: grid;
  gap: 10px;
}
.app-plugin-backup-password__origin,
.app-plugin-backup-password__hint {
  margin: 0;
  color: var(--app-text-muted);
  font-size: 12px;
}
.app-plugin-backup-password__intro {
  margin: 0;
  color: var(--app-text);
  font-size: 14px;
  line-height: 1.6;
}
.app-plugin-backup-password__field {
  display: grid;
  gap: 4px;
  color: var(--app-text);
  font-size: 13px;
}
.app-plugin-backup-password__error {
  margin: 0;
  color: var(--el-color-danger);
  font-size: 12px;
}
</style>
