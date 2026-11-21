<script setup lang="ts">
import { computed, ref } from "vue";
import { ElMessage } from "element-plus";
import { Delete, Plus, Refresh, VideoPlay } from "@element-plus/icons-vue";
import { useVueI18n } from "@/i18n/messages";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

interface MockRule {
  method: string;
  path: string;
  status: number;
  body: string;
  headers: [string, string][];
  delay_ms: number;
}

interface MockServerInfo {
  id: string;
  port: number;
  rules: MockRule[];
  running: boolean;
}

const props = defineProps<{ modelValue: boolean }>();
const emit = defineEmits<{ "update:modelValue": [value: boolean] }>();
const { t } = useVueI18n();

const methods = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "*"];
const port = ref(8080);
const rules = ref<MockRule[]>([createRule()]);
const servers = ref<MockServerInfo[]>([]);
const loading = ref(false);
const starting = ref(false);
const stoppingId = ref<string>();

const visible = computed({
  get: () => props.modelValue,
  set: (value: boolean) => emit("update:modelValue", value),
});

function createRule(): MockRule {
  return {
    method: "GET",
    path: "/api/example",
    status: 200,
    body: '{"ok":true}',
    headers: [],
    delay_ms: 0,
  };
}

function addRule() {
  rules.value.push(createRule());
}

function deleteRule(index: number) {
  rules.value.splice(index, 1);
}

function ensureDesktop() {
  if (isDesktopRuntime()) return true;
  ElMessage.warning(t("common.desktopRequired"));
  return false;
}

async function refreshServers() {
  if (!ensureDesktop()) return;
  loading.value = true;
  try {
    servers.value = await invokeBackend<MockServerInfo[]>("mock_server_list");
  } catch (error) {
    ElMessage.error(String(error));
  } finally {
    loading.value = false;
  }
}

function validateRules() {
  if (!Number.isInteger(port.value) || port.value < 1 || port.value > 65535) {
    ElMessage.warning(t("http.mockPortInvalid"));
    return false;
  }
  if (!rules.value.length) {
    ElMessage.warning(t("http.mockRuleRequired"));
    return false;
  }

  for (const rule of rules.value) {
    rule.method = rule.method.toUpperCase();
    rule.path = rule.path.trim();
    rule.status = Number(rule.status);
    rule.delay_ms = Number(rule.delay_ms);

    if (!methods.includes(rule.method) || !rule.path.startsWith("/")) {
      ElMessage.warning(t("http.mockRuleInvalid"));
      return false;
    }
    if (!Number.isInteger(rule.status) || rule.status < 100 || rule.status > 599) {
      ElMessage.warning(t("http.mockStatusInvalid"));
      return false;
    }
    if (!Number.isFinite(rule.delay_ms) || rule.delay_ms < 0 || rule.delay_ms > 60000) {
      ElMessage.warning(t("http.mockDelayInvalid"));
      return false;
    }
  }

  return true;
}

async function startServer() {
  if (!ensureDesktop() || !validateRules()) return;

  starting.value = true;
  try {
    const serverId = await invokeBackend<string>("mock_server_start", {
      port: port.value,
      rules: rules.value,
    });
    await refreshServers();
    const started = servers.value.find((server) => server.id === serverId);
    ElMessage.success(t("http.mockStarted", { port: started?.port ?? port.value }));
  } catch (error) {
    ElMessage.error(String(error));
  } finally {
    starting.value = false;
  }
}

async function stopServer(serverId: string) {
  if (!ensureDesktop()) return;

  stoppingId.value = serverId;
  try {
    await invokeBackend<void>("mock_server_stop", { serverId });
    await refreshServers();
    ElMessage.success(t("http.mockStopped"));
  } catch (error) {
    ElMessage.error(String(error));
  } finally {
    stoppingId.value = undefined;
  }
}

function handleOpened() {
  void refreshServers();
}
</script>

<template>
  <el-dialog
    v-model="visible"
    :title="t('http.mockServerTitle')"
    width="min(900px, 94vw)"
    top="4vh"
    class="http-mock-dialog http-fit-dialog"
    append-to-body
    @opened="handleOpened"
  >
    <div class="http-mock-panel">
      <el-alert :title="t('http.mockServerHint')" type="info" :closable="false" show-icon />

      <section class="http-mock-section">
        <div class="http-mock-section__header">
          <div>
            <h3>{{ t("http.mockRunningServers") }}</h3>
            <p>{{ t("http.mockRunningHint") }}</p>
          </div>
          <el-button :icon="Refresh" :loading="loading" @click="refreshServers">
            {{ t("common.refresh") }}
          </el-button>
        </div>

        <div v-if="servers.length" class="http-mock-running-list">
          <div v-for="server in servers" :key="server.id" class="http-mock-running-card">
            <div>
              <strong>http://127.0.0.1:{{ server.port }}</strong>
              <span>{{ t("http.ruleCount", { count: server.rules.length }) }}</span>
            </div>
            <el-button
              type="danger"
              plain
              size="small"
              :loading="stoppingId === server.id"
              @click="stopServer(server.id)"
            >
              {{ t("http.stop") }}
            </el-button>
          </div>
        </div>
        <el-empty v-else :description="t('http.mockNoRunningServers')" :image-size="48" />
      </section>

      <section class="http-mock-section">
        <div class="http-mock-section__header">
          <div>
            <h3>{{ t("http.mockCreateServer") }}</h3>
            <p>{{ t("http.mockCreateHint") }}</p>
          </div>
          <el-form-item :label="t('http.port')" class="http-mock-port">
            <el-input-number v-model="port" :min="1" :max="65535" :controls="false" />
          </el-form-item>
        </div>

        <div class="http-mock-rules-header">
          <div>
            <strong>{{ t("http.mockRules") }}</strong>
            <span>{{ t("http.ruleCount", { count: rules.length }) }}</span>
          </div>
          <el-button size="small" :icon="Plus" @click="addRule">
            {{ t("http.addRuleBtn") }}
          </el-button>
        </div>

        <div v-for="(rule, index) in rules" :key="index" class="http-mock-rule">
          <div class="http-mock-rule__fields">
            <el-select v-model="rule.method" :aria-label="t('http.method')">
              <el-option v-for="method in methods" :key="method" :label="method" :value="method" />
            </el-select>
            <el-input
              v-model="rule.path"
              placeholder="/api/path"
              :aria-label="t('http.mockPath')"
            />
            <el-input-number v-model="rule.status" :min="100" :max="599" :controls="false" />
            <el-input-number
              v-model="rule.delay_ms"
              :min="0"
              :max="60000"
              :controls="false"
              :placeholder="t('http.delayPlaceholder')"
            />
            <span class="http-mock-rule__unit">ms</span>
            <el-button
              text
              type="danger"
              :icon="Delete"
              :aria-label="t('http.deleteRule')"
              :disabled="rules.length === 1"
              @click="deleteRule(index)"
            />
          </div>
          <el-input
            v-model="rule.body"
            type="textarea"
            :autosize="{ minRows: 3, maxRows: 8 }"
            :placeholder="t('http.responseBodyPlaceholder')"
          />
        </div>

        <div class="http-mock-actions">
          <el-button type="primary" :icon="VideoPlay" :loading="starting" @click="startServer">
            {{ t("http.startMockBtn") }}
          </el-button>
        </div>
      </section>
    </div>
  </el-dialog>
</template>

<style scoped>
.http-mock-panel {
  display: grid;
  gap: 18px;
}

.http-mock-section {
  border: 1px solid var(--app-border);
  border-radius: 8px;
  background: var(--app-surface);
  padding: 14px;
}

.http-mock-section__header,
.http-mock-rules-header,
.http-mock-running-card,
.http-mock-running-card > div,
.http-mock-rules-header > div,
.http-mock-actions {
  display: flex;
  align-items: center;
}

.http-mock-section__header,
.http-mock-rules-header,
.http-mock-running-card {
  justify-content: space-between;
  gap: 12px;
}

.http-mock-section__header {
  min-height: 34px;
  margin-bottom: 12px;
}

.http-mock-section h3,
.http-mock-section p {
  margin: 0;
}

.http-mock-section h3 {
  color: var(--app-text);
  font-size: 13px;
  font-weight: 600;
}

.http-mock-section p {
  margin-top: 3px;
  color: var(--app-text-muted);
  font-size: 12px;
}

.http-mock-port {
  margin: 0;
}

.http-mock-port :deep(.el-form-item__label) {
  color: var(--app-text-muted);
  font-size: 12px;
}

.http-mock-port :deep(.el-input-number) {
  width: 105px;
}

.http-mock-running-list,
.http-mock-rule {
  display: grid;
  gap: 8px;
}

.http-mock-running-list {
  gap: 6px;
}

.http-mock-running-card {
  min-height: 48px;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-muted);
  padding: 8px 10px;
}

.http-mock-running-card > div,
.http-mock-rules-header > div {
  gap: 8px;
}

.http-mock-running-card strong {
  color: var(--app-accent);
  font:
    600 12px/1.4 ui-monospace,
    SFMono-Regular,
    Menlo,
    monospace;
}

.http-mock-running-card span,
.http-mock-rules-header span {
  border-radius: 10px;
  background: var(--app-hover);
  padding: 2px 7px;
  color: var(--app-text-muted);
  font-size: 11px;
}

.http-mock-rules-header {
  margin-bottom: 8px;
}

.http-mock-rules-header strong {
  color: var(--app-text);
  font-size: 12px;
}

.http-mock-rule {
  margin-top: 10px;
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-surface);
  padding: 10px;
}

.http-mock-rule__fields {
  display: grid;
  grid-template-columns: 110px minmax(180px, 1fr) 82px 96px 18px 28px;
  align-items: center;
  gap: 7px;
}

.http-mock-rule__fields :deep(.el-input-number) {
  width: 100%;
}

.http-mock-rule__unit {
  color: var(--app-text-muted);
  font-size: 11px;
}

.http-mock-actions {
  justify-content: flex-end;
  margin-top: 14px;
}

@media (max-width: 720px) {
  .http-mock-rule__fields {
    grid-template-columns: 1fr 1fr;
  }

  .http-mock-rule__unit {
    display: none;
  }
}
</style>
