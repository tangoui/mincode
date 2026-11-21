<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { ElMessageBox } from "element-plus";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import { useVueI18n } from "@/i18n/messages";
import { copyText, isDesktopRuntime } from "@/services/desktop-bridge";
import { requestSshHistory } from "@/services/ssh-command-history-service";
import type { SshHistoryEntry, SshHistoryRequest } from "@/domain/ssh/command-history";
import type { SshConnectionProfile } from "@/domain/ssh/types";

const props = defineProps<{ sessionId: string; profile: SshConnectionProfile; ready: boolean }>();
const emit = defineEmits<{ fill: [command: string] }>();
const { t } = useVueI18n();
const desktop = isDesktopRuntime();
const entries = ref<SshHistoryEntry[]>([]), enabled = ref(true), busy = ref(false), error = ref("");
const query = ref(""), favorites = ref(false), page = ref(1);
const filtered = computed(() => entries.value.filter(e => (!favorites.value || e.favorite) && e.command.toLowerCase().includes(query.value.toLowerCase())));
const visible = computed(() => filtered.value.slice((page.value - 1) * 50, page.value * 50));

async function call(request: SshHistoryRequest) {
  if (busy.value || !desktop) return false;
  busy.value = true;
  error.value = "";
  try {
    const data = await requestSshHistory({
      sessionId: props.sessionId, profileId: props.profile.id, host: props.profile.host,
      port: props.profile.port, username: props.profile.username,
    }, request);
    entries.value = data.entries;
    enabled.value = data.enabled;
    page.value = Math.min(page.value, Math.max(1, Math.ceil(filtered.value.length / 50)));
    return true;
  } catch (e) {
    error.value = String(e);
    return false;
  } finally { busy.value = false; }
}
async function clear() {
  try {
    await ElMessageBox.confirm(t("ssh.history.clearConfirm"), t("ssh.history.clear"), {
      type: "warning", confirmButtonText: t("common.clear"), cancelButtonText: t("common.cancel"),
    });
  } catch { return; }
  await call({ action: "update", operation: "clear" });
}
async function toggleSave(value: boolean) {
  await call({ action: "update", operation: "setEnabled", enabled: Boolean(value) });
}
async function fill(entry: SshHistoryEntry) {
  try {
    const { value } = await ElMessageBox.prompt(t("ssh.history.fillConfirm"), t("ssh.history.fillTitle"), {
      inputValue: entry.command, confirmButtonText: t("ssh.history.fillOnly"), cancelButtonText: t("common.cancel"),
      inputValidator: value => !!value && !/[\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/u.test(value) || t("ssh.history.singleLine"),
    });
    if (props.ready) emit("fill", value);
  } catch { /* cancelled */ }
}
async function copy(command: string) { try { await copyText(command); } catch (e) { error.value = String(e); } }
onMounted(() => { void call({ action: "list" }); });
</script>
<template>
  <section class="ssh-command-history" :aria-busy="busy">
    <el-alert type="info" :closable="false" :title="t('ssh.history.privacy')" :description="t('ssh.history.help')" />
    <el-alert v-if="!desktop" type="warning" :closable="false" :title="t('ssh.history.desktopOnly')" />
    <p>{{ profile.username }}@{{ profile.host }}:{{ profile.port }}</p>
    <el-alert v-if="error" type="error" :title="error" :closable="false" />
    <div class="history-actions">
      <el-switch :disabled="busy || !desktop" :model-value="enabled" :active-text="t('ssh.history.saveToggle')" @change="toggleSave" />
      <el-button :disabled="busy || !entries.length" type="danger" plain @click="clear">{{ t('ssh.history.clear') }}</el-button>
    </div>
    <AppSearchField v-model="query" :placeholder="t('ssh.history.search')" :clear-label="t('common.clear')" clearable @input="page = 1" />
    <el-checkbox v-model="favorites" @change="page = 1">{{ t('ssh.history.favoritesOnly') }}</el-checkbox>
    <el-empty v-if="!filtered.length" :description="t('ssh.history.empty')" />
    <article v-for="entry in visible" :key="entry.id" class="history-entry">
      <pre>{{ entry.command }}</pre>
      <small>{{ t('ssh.history.firstSaved') }} · {{ new Date(entry.importedAt).toLocaleString() }}</small>
      <div>
        <el-button size="small" :disabled="!ready || busy" @click="fill(entry)">{{ t('ssh.history.fill') }}</el-button>
        <el-button size="small" @click="copy(entry.command)">{{ t('common.copy') }}</el-button>
        <el-button size="small" :disabled="busy" @click="call({ action: 'update', operation: 'toggleFavorite', id: entry.id })">{{ t(entry.favorite ? 'ssh.history.unfavorite' : 'ssh.history.favorite') }}</el-button>
        <el-button size="small" :disabled="busy" @click="call({ action: 'update', operation: 'delete', id: entry.id })">{{ t('ssh.history.delete') }}</el-button>
      </div>
    </article>
    <el-pagination v-model:current-page="page" :page-size="50" :total="filtered.length" layout="prev, pager, next, total" />
  </section>
</template>
<style scoped>
.ssh-command-history { display: grid; gap: 12px; }
.history-actions { display: flex; flex-wrap: wrap; gap: 8px; }
.history-entry { padding: 12px 0; border-bottom: 1px solid var(--app-border); }
.history-entry pre { white-space: pre-wrap; overflow-wrap: anywhere; user-select: text; }
.history-entry small { display: block; margin-bottom: 8px; color: var(--app-text-muted); }
</style>
