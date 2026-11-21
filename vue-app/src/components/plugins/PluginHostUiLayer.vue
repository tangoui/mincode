<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, ref, watch } from "vue";
import { useVueI18n } from "@/i18n/messages";
import { filterQuickPickItems, SERVICE_PROVIDER_REMEMBER_ID, type PluginPrompt } from "@/domain/plugins/host-prompts";
import { diffModelPath, utf8Length } from "@/domain/plugins/host-ui";
import type { BackupDialogAnswer } from "@/services/backup-host";
import { answerBackupDialog, answerPrompt } from "@/services/plugin-host-ui";
import { DEFAULT_EDITOR_PREFERENCES } from "@/domain/projects/types";
import { usePluginHostUiStore } from "@/stores/plugin-host-ui";

const MonacoDiffEditor = defineAsyncComponent(() => import("@/components/ui/MonacoDiffEditor.vue"));
// Backup dialogs (services/backup-host.ts) load with the first backup request.
const PluginBackupPasswordDialog = defineAsyncComponent(() => import("@/components/plugins/PluginBackupPasswordDialog.vue"));
const PluginBackupRestoreConfirm = defineAsyncComponent(() => import("@/components/plugins/PluginBackupRestoreConfirm.vue"));

const { t } = useVueI18n();
const store = usePluginHostUiStore();

const prompt = computed<PluginPrompt | undefined>(() => store.prompts[0]);
const backupDialog = computed(() => store.backupDialogs[0]);

function answerBackup(answer: BackupDialogAnswer) {
  if (backupDialog.value) void answerBackupDialog(backupDialog.value, answer);
}
const query = ref("");
const picked = ref<string[]>([]);
const active = ref("");
const value = ref("");
const provider = ref("");
const rememberProvider = ref(false);
const queryInput = ref<{ focus: () => void }>();
const valueInput = ref<{ focus: () => void }>();

const items = computed(() => (prompt.value?.kind === "quickPick" ? filterQuickPickItems(prompt.value.items, query.value) : []));
const listboxId = computed(() => `plugin-host-options-${prompt.value?.id ?? "none"}`);
const activeIndex = computed(() => items.value.findIndex((item) => item.id === active.value));
const activeOptionId = computed(() => (activeIndex.value >= 0 ? `${listboxId.value}-${activeIndex.value}` : undefined));
const MAX_VALUE_BYTES = 4096;
const valueTooLong = computed(() => prompt.value?.kind === "inputBox" && utf8Length(value.value) > MAX_VALUE_BYTES);
const dialogTitle = computed(() => {
  const current = prompt.value;
  if (current?.kind === "serviceProvider") return t("plugins.services.chooseProvider");
  if (current?.kind === "serviceConfirm") return t("plugins.services.confirmTitle");
  return current?.title || current?.pluginName;
});
// The host's own service prompts are not "from" a plugin.
const fromPlugin = computed(() => !(prompt.value?.kind === "serviceProvider" || prompt.value?.kind === "serviceConfirm") || !prompt.value.fromHost);

watch(prompt, (next) => {
  query.value = "";
  picked.value = [];
  active.value = next?.kind === "quickPick" ? next.items[0]?.id ?? "" : "";
  value.value = next?.kind === "inputBox" ? next.value : "";
  provider.value = next?.kind === "serviceProvider" ? next.items[0]?.id ?? "" : "";
  rememberProvider.value = false;
  void nextTick(() => (next?.kind === "inputBox" ? valueInput.value?.focus() : queryInput.value?.focus()));
}, { immediate: true });

watch(items, (list) => {
  if (!list.some((item) => item.id === active.value)) active.value = list[0]?.id ?? "";
});

function cancel() {
  if (prompt.value) void answerPrompt(prompt.value, { cancelled: true });
}

function choose(id: string) {
  const current = prompt.value;
  if (current?.kind !== "quickPick") return;
  if (!current.canPickMany) {
    void answerPrompt(current, { ids: [id] });
    return;
  }
  picked.value = picked.value.includes(id) ? picked.value.filter((item) => item !== id) : [...picked.value, id];
}

function confirm() {
  const current = prompt.value;
  if (!current || (current.kind !== "quickPick" && current.kind !== "inputBox")) return;
  if (current.kind === "inputBox") { if (!valueTooLong.value) void answerPrompt(current, { value: value.value }); }
  else if (current.canPickMany) { if (picked.value.length) void answerPrompt(current, { ids: picked.value }); }
  else if (active.value) void answerPrompt(current, { ids: [active.value] });
}

function allowCall(choice: "once" | "always") {
  const kind = prompt.value?.kind;
  if (kind === "pluginCall" || kind === "databaseSchema" || kind === "sshFiles") void answerPrompt(prompt.value!, { ids: [choice] });
}

function chooseProvider() {
  const current = prompt.value;
  if (current?.kind !== "serviceProvider" || !provider.value) return;
  void answerPrompt(current, { ids: rememberProvider.value ? [provider.value, SERVICE_PROVIDER_REMEMBER_ID] : [provider.value] });
}

function runService() {
  if (prompt.value?.kind === "serviceConfirm") void answerPrompt(prompt.value, { ids: ["once"] });
}

function riskTag(risk: string) {
  return risk === "dangerous" ? "danger" : risk === "write" ? "warning" : "info";
}

function moveActive(step: number) {
  const list = items.value;
  if (!list.length) return;
  const index = list.findIndex((item) => item.id === active.value);
  active.value = list[(index + step + list.length) % list.length]!.id;
  void nextTick(() => {
    if (activeOptionId.value) document.getElementById(activeOptionId.value)?.scrollIntoView({ block: "nearest" });
  });
}

/** Enter that confirms an input-method composition is not a submit. */
function composing(event: KeyboardEvent) {
  return event.isComposing || event.keyCode === 229;
}

function onValueEnter(event: KeyboardEvent) {
  if (composing(event)) return;
  event.preventDefault();
  confirm();
}

function onPickKey(event: KeyboardEvent) {
  if (composing(event)) return;
  if (event.key === "ArrowDown" || event.key === "ArrowUp") {
    event.preventDefault();
    moveActive(event.key === "ArrowDown" ? 1 : -1);
  } else if (event.key === "Enter") {
    event.preventDefault();
    if (prompt.value?.kind === "quickPick" && prompt.value.canPickMany && active.value && !event.ctrlKey && !event.metaKey) choose(active.value);
    else confirm();
  }
}
</script>

<template>
  <!-- One dialog instance per prompt: closing one with Esc must not leave the next hidden. -->
  <el-dialog
    :key="prompt?.id ?? 'none'"
    :model-value="Boolean(prompt)"
    class="plugin-host-prompt"
    width="560px"
    align-center
    append-to-body
    :close-on-click-modal="false"
    :title="dialogTitle"
    @close="cancel"
  >
    <template v-if="prompt">
      <p v-if="fromPlugin" class="plugin-host-prompt__origin">{{ t("pluginHostUi.promptFrom", { plugin: prompt.pluginName }) }}</p>
      <template v-if="prompt.kind === 'quickPick'">
        <el-input
          ref="queryInput"
          v-model="query"
          :placeholder="prompt.placeholder || t('pluginHostUi.quickPickFilter')"
          :aria-label="t('pluginHostUi.quickPickFilter')"
          :aria-controls="listboxId"
          :aria-activedescendant="activeOptionId"
          clearable
          @keydown="onPickKey"
        />
        <p v-if="prompt.canPickMany" class="plugin-host-prompt__hint">{{ t("pluginHostUi.quickPickManyHint") }}</p>
        <el-scrollbar max-height="320px" class="plugin-host-prompt__list">
          <div :id="listboxId" role="listbox" :aria-multiselectable="prompt.canPickMany" :aria-label="prompt.title || prompt.pluginName">
            <div
              v-for="(item, index) in items"
              :id="`${listboxId}-${index}`"
              :key="item.id"
              role="option"
              class="plugin-host-prompt__item"
              :class="{ 'is-active': item.id === active, 'is-picked': picked.includes(item.id) }"
              :aria-selected="prompt.canPickMany ? picked.includes(item.id) : item.id === active"
              @mouseenter="active = item.id"
              @click="choose(item.id)"
            >
              <el-checkbox v-if="prompt.canPickMany" :model-value="picked.includes(item.id)" tabindex="-1" @click.stop="choose(item.id)" />
              <span class="plugin-host-prompt__text">
                <span class="plugin-host-prompt__label">{{ item.label }}<small v-if="item.description">{{ item.description }}</small></span>
                <span v-if="item.detail" class="plugin-host-prompt__detail">{{ item.detail }}</span>
              </span>
            </div>
            <p v-if="!items.length" class="plugin-host-prompt__empty">{{ t("pluginHostUi.quickPickEmpty") }}</p>
          </div>
        </el-scrollbar>
      </template>
      <template v-else-if="prompt.kind === 'pluginCall'">
        <p class="plugin-host-prompt__call">{{ t("pluginHostUi.callBody", { plugin: prompt.pluginName, target: prompt.call.targetName, action: prompt.call.action }) }}</p>
        <p v-if="prompt.call.description" class="plugin-host-prompt__hint">{{ prompt.call.description }}</p>
        <p class="plugin-host-prompt__risk">
          <el-tag size="small" :type="riskTag(prompt.call.risk)">{{ t(`pluginHostUi.risk.${prompt.call.risk}`) }}</el-tag>
          <span>{{ t(prompt.canRemember ? "pluginHostUi.callRememberHint" : "pluginHostUi.callEachTimeHint", { target: prompt.call.targetName }) }}</span>
        </p>
        <pre class="plugin-host-prompt__payload">{{ prompt.call.payload }}</pre>
      </template>
      <template v-else-if="prompt.kind === 'serviceProvider'">
        <p class="plugin-host-prompt__call">{{ t("plugins.services.chooseProviderBody", { service: prompt.service }) }}</p>
        <el-scrollbar max-height="320px">
          <el-radio-group v-model="provider" class="plugin-host-prompt__providers" :aria-label="t('plugins.services.chooseProvider')">
            <el-radio v-for="item in prompt.items" :key="item.id" :value="item.id" border class="plugin-host-prompt__provider">
              <span class="plugin-host-prompt__text">
                <span class="plugin-host-prompt__label">{{ item.label }}<small v-if="item.description">{{ item.description }}</small></span>
                <span v-if="item.detail" class="plugin-host-prompt__detail">{{ item.detail }}</span>
              </span>
            </el-radio>
          </el-radio-group>
        </el-scrollbar>
        <el-checkbox v-model="rememberProvider" class="plugin-host-prompt__remember">{{ t("plugins.services.rememberProvider") }}</el-checkbox>
      </template>
      <template v-else-if="prompt.kind === 'serviceConfirm'">
        <p class="plugin-host-prompt__call">{{ t("plugins.services.confirmBody", { service: prompt.call.serviceTitle, provider: prompt.call.targetName }) }}</p>
        <p v-if="prompt.call.description" class="plugin-host-prompt__hint">{{ prompt.call.description }}</p>
        <p class="plugin-host-prompt__risk">
          <el-tag size="small" :type="riskTag(prompt.call.risk)">{{ t(`pluginHostUi.risk.${prompt.call.risk}`) }}</el-tag>
          <span>{{ t("plugins.services.confirmHint") }}</span>
        </p>
        <pre v-if="prompt.call.payload" class="plugin-host-prompt__payload">{{ prompt.call.payload }}</pre>
      </template>
      <template v-else-if="prompt.kind === 'databaseSchema'">
        <p class="plugin-host-prompt__call">{{ t("pluginHostUi.databaseBody", { plugin: prompt.pluginName, connection: prompt.database.name }) }}</p>
        <p class="plugin-host-prompt__risk">
          <el-tag size="small" type="info">{{ prompt.database.kind }}</el-tag>
          <span v-if="prompt.database.database">{{ prompt.database.database }}</span>
        </p>
        <p class="plugin-host-prompt__hint">{{ t("pluginHostUi.databaseScopeHint") }}</p>
      </template>
      <template v-else-if="prompt.kind === 'sshFiles'">
        <p class="plugin-host-prompt__call">{{ t("plugins.sshFiles.body", { plugin: prompt.pluginName, connection: prompt.sshFiles.connectionName }) }}</p>
        <p class="plugin-host-prompt__risk">
          <el-tag size="small" type="info">SFTP</el-tag>
          <span>{{ prompt.sshFiles.username }}@{{ prompt.sshFiles.host }}:{{ prompt.sshFiles.port }}</span>
        </p>
        <pre class="plugin-host-prompt__payload">{{ prompt.sshFiles.directory }}</pre>
        <p class="plugin-host-prompt__hint">{{ t("plugins.sshFiles.scopeHint") }}</p>
      </template>
      <template v-else>
        <p v-if="prompt.prompt" class="plugin-host-prompt__hint">{{ prompt.prompt }}</p>
        <el-input
          ref="valueInput"
          v-model="value"
          :type="prompt.password ? 'password' : 'text'"
          :show-password="prompt.password"
          :placeholder="prompt.placeholder"
          :aria-label="prompt.title"
          autocomplete="off"
          @keydown.enter="onValueEnter"
        />
        <p v-if="valueTooLong" class="plugin-host-prompt__error">{{ t("pluginHostUi.inputTooLong") }}</p>
      </template>
    </template>
    <template v-if="prompt?.kind === 'databaseSchema'" #footer>
      <el-button @click="cancel">{{ t("pluginHostUi.callDeny") }}</el-button>
      <el-button v-if="prompt.canRemember" @click="allowCall('always')">{{ t("pluginHostUi.databaseAlways") }}</el-button>
      <el-button type="primary" @click="allowCall('once')">{{ t("pluginHostUi.databaseSession") }}</el-button>
    </template>
    <template v-else-if="prompt?.kind === 'sshFiles'" #footer>
      <el-button @click="cancel">{{ t("pluginHostUi.callDeny") }}</el-button>
      <el-button v-if="prompt.canRemember" @click="allowCall('always')">{{ t("plugins.sshFiles.always") }}</el-button>
      <el-button type="primary" @click="allowCall('once')">{{ t("plugins.sshFiles.session") }}</el-button>
    </template>
    <template v-else-if="prompt?.kind === 'pluginCall'" #footer>
      <el-button @click="cancel">{{ t("pluginHostUi.callDeny") }}</el-button>
      <el-button v-if="prompt.canRemember" @click="allowCall('always')">{{ t("pluginHostUi.callAlways") }}</el-button>
      <el-button :type="prompt.call.risk === 'dangerous' ? 'danger' : 'primary'" @click="allowCall('once')">{{ t("pluginHostUi.callOnce") }}</el-button>
    </template>
    <template v-else-if="prompt?.kind === 'serviceProvider'" #footer>
      <el-button @click="cancel">{{ t("common.cancel") }}</el-button>
      <el-button type="primary" :disabled="!provider" @click="chooseProvider">{{ t("common.confirm") }}</el-button>
    </template>
    <template v-else-if="prompt?.kind === 'serviceConfirm'" #footer>
      <el-button @click="cancel">{{ t("common.cancel") }}</el-button>
      <el-button type="danger" @click="runService">{{ t("plugins.services.confirmRun") }}</el-button>
    </template>
    <template v-else #footer>
      <el-button @click="cancel">{{ t("common.cancel") }}</el-button>
      <el-button
        type="primary"
        :disabled="(prompt?.kind === 'quickPick' && (prompt.canPickMany ? !picked.length : !active)) || valueTooLong"
        @click="confirm"
      >{{ t("common.confirm") }}</el-button>
    </template>
  </el-dialog>

  <!-- One backup dialog at a time; keyed so the next request gets a fresh instance. -->
  <PluginBackupPasswordDialog
    v-if="backupDialog?.kind === 'backupPassword'"
    :key="backupDialog.id"
    :request="backupDialog"
    @submit="answerBackup"
    @cancel="answerBackup({ cancelled: true })"
  />
  <PluginBackupRestoreConfirm
    v-else-if="backupDialog?.kind === 'backupRestoreConfirm'"
    :key="backupDialog.id"
    :request="backupDialog"
    @confirm="answerBackup({ confirmed: true })"
    @cancel="answerBackup({ confirmed: false })"
  />

  <el-dialog
    v-for="diff in store.diffs"
    :key="diff.key"
    :model-value="true"
    class="plugin-host-diff"
    width="min(1100px, 92vw)"
    append-to-body
    :title="diff.title"
    @close="store.closeDiff(diff.key)"
  >
    <div class="plugin-host-diff__head">
      <span>{{ diff.left.title || t("pluginHostUi.diffLeft") }}</span>
      <span>{{ diff.right.title || t("pluginHostUi.diffRight") }}</span>
    </div>
    <div class="plugin-host-diff__editor">
      <MonacoDiffEditor
        :path="diffModelPath(diff.key, diff.language)"
        :original-content="diff.left.content"
        :modified-content="diff.right.content"
        :preferences="DEFAULT_EDITOR_PREFERENCES"
        navigation
      />
    </div>
    <p class="plugin-host-diff__origin">{{ t("pluginHostUi.diffFrom", { plugin: diff.pluginName }) }}</p>
  </el-dialog>
</template>

<style scoped>
.plugin-host-prompt__origin,
.plugin-host-diff__origin {
  margin: 0 0 10px;
  color: var(--app-text-muted);
  font-size: 12px;
}
.plugin-host-diff__origin { margin: 10px 0 0; }
.plugin-host-prompt__list { margin-top: 10px; }
.plugin-host-prompt__item {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 8px 10px;
  border-radius: 8px;
  cursor: pointer;
}
.plugin-host-prompt__item.is-active { background: var(--app-muted); }
.plugin-host-prompt__text { display: grid; gap: 2px; min-width: 0; }
.plugin-host-prompt__label { font-size: 14px; color: var(--app-text); overflow-wrap: anywhere; }
.plugin-host-prompt__label small { margin-left: 8px; color: var(--app-text-muted); font-size: 12px; }
.plugin-host-prompt__detail { color: var(--app-text-muted); font-size: 12px; overflow-wrap: anywhere; }
.plugin-host-prompt__empty,
.plugin-host-prompt__hint { margin: 8px 0; color: var(--app-text-muted); font-size: 13px; }
.plugin-host-prompt__providers { display: grid; gap: 8px; width: 100%; }
/* A bordered radio is one line tall by default; providers may wrap a detail line. */
.plugin-host-prompt__provider { height: auto; margin: 0; padding: 8px 12px; white-space: normal; }
.plugin-host-prompt__remember { margin-top: 10px; }
.plugin-host-prompt__call { margin: 0 0 8px; color: var(--app-text); font-size: 14px; line-height: 1.6; }
.plugin-host-prompt__error { margin: 6px 0 0; color: var(--el-color-danger); font-size: 12px; }
.plugin-host-prompt__risk { display: flex; align-items: center; gap: 8px; margin: 8px 0; color: var(--app-text-muted); font-size: 12px; }
.plugin-host-prompt__payload {
  max-height: 180px;
  margin: 0;
  padding: 8px 10px;
  overflow: auto;
  border-radius: 8px;
  background: var(--app-muted);
  font-family: var(--app-font-mono, monospace);
  font-size: 12px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.plugin-host-diff__head {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
  margin-bottom: 6px;
  color: var(--app-text-muted);
  font-size: 12px;
}
.plugin-host-diff__editor {
  height: min(60vh, 560px);
  border: 1px solid var(--app-border);
  border-radius: 8px;
  overflow: hidden;
}
</style>
