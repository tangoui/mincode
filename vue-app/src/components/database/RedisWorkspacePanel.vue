<script setup lang="ts">
import { computed, onBeforeUnmount, onDeactivated, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import { CopyDocument, Delete, MagicStick, Plus, Refresh } from "@element-plus/icons-vue";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import { claimContextMenuKey, contextMenuAnchor } from "@/components/ui/app-context-menu";
import PluginMenuItems from "@/components/plugins/PluginMenuItems.vue";
import { usePluginMenuTarget } from "@/composables/use-plugin-menu-target";
import { copyText } from "@/services/desktop-bridge";
import { useVueI18n } from "@/i18n/messages";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import { registerAiWorkspaceActionHandler } from "@/services/ai-workspace-actions";

const store = useDatabaseWorkspaceStore();
const { t } = useVueI18n();
const editorValue = ref("");
const editorType = ref("string");
const editorTtl = ref(-1);
// 加载时的序列化值与 TTL，用于判断用户实际改了什么，只改 TTL 时不回写值。
const loadedValue = ref("");
const loadedTtl = ref(-1);
// 含非 UTF-8 字节的值经 JSON 传输已被替换成 U+FFFD，回写会破坏原值，只允许改 TTL。
const valueReadOnly = computed(() => Boolean(store.redisKeyInfo?.binary));
// Switching keys clears the store's key info until the new one arrives. Meanwhile the pane keeps showing the
// previous key, locked (read-only, no save/delete), instead of flashing the "select a key" placeholder; a
// spinner appears only when the load is slow.
const loadingKey = ref("");
const shownKey = ref("");
const keyLoading = computed(() => loadingKey.value !== "" && loadingKey.value === store.selectedRedisKey && !store.redisKeyInfo);
const showKeySpinner = ref(false);
const displayKey = computed(() => (store.redisKeyInfo ? store.selectedRedisKey : shownKey.value));
let keySpinnerTimer: ReturnType<typeof setTimeout> | undefined;
const createVisible = ref(false);
const createKey = ref("");
const createType = ref("string");
const createValue = ref("");
const createTtl = ref(-1);

const hasMore = computed(() => store.redisKeys.nextCursor !== "0");

// Key row context menu: "Copy key name" plus plugin items (contributes.menus redis/key/context).
// The target is the listed row (never the value) of the connection and database the list belongs to.
const keyMenu = ref<{ key: string; x: number; y: number } | null>(null);
const keyMenuItem = computed(() => {
  const key = keyMenu.value?.key;
  return key === undefined ? undefined : store.redisKeys.keys.find((item) => item.key === key);
});
const { host: keyPluginMenuHost, context: keyPluginMenuContext } = usePluginMenuTarget("redis/key/context", () => {
  const item = keyMenuItem.value;
  const connection = store.activeConnection;
  return item && connection
    ? { connection: { id: connection.id, name: connection.name }, dbIndex: store.redisDbIndex, key: item.key, type: item.type, ttl: item.ttl }
    : null;
});
// The menu belongs to one listed key of one connection and database: close it when the key leaves the
// list (refresh, delete) or the connection, session or database changes.
watch(keyMenuItem, (item) => {
  if (!item) keyMenu.value = null;
});
watch([() => store.activeConnectionId, () => store.activeSessionId, () => store.redisDbIndex], () => {
  keyMenu.value = null;
});
// The database view is KeepAlive'd: leaving it must not leave the menu (and its target) open.
onDeactivated(() => {
  keyMenu.value = null;
});

function openKeyMenu(event: MouseEvent | KeyboardEvent, key: string) {
  keyMenu.value = { key, ...contextMenuAnchor(event) };
  // Same as clicking the row (which also retries a key that failed to load); an open key is not
  // reloaded, which would drop unsaved edits.
  if (key !== store.selectedRedisKey || (!store.redisKeyInfo && !keyLoading.value)) void selectKey(key);
}

// Roving tabindex: every row is at tabindex -1 except one (the last focused,
// else the selected, else the first) takes the tab stop and the arrow keys move between rows.
const focusedKey = ref<string>();
const tabStopKey = computed(() => {
  const keys = store.redisKeys.keys;
  const listed = (key: string | undefined) => key !== undefined && keys.some((item) => item.key === key);
  if (listed(focusedKey.value)) return focusedKey.value;
  return listed(store.selectedRedisKey) ? store.selectedRedisKey : keys[0]?.key;
});

function handleKeyRowKeydown(event: KeyboardEvent, key: string) {
  const row = event.currentTarget as HTMLElement;
  let next: Element | null | undefined;
  if (claimContextMenuKey(event)) {
    openKeyMenu(event, key);
    return;
  }
  switch (event.key) {
    case "ArrowDown":
      next = row.nextElementSibling;
      break;
    case "ArrowUp":
      next = row.previousElementSibling;
      break;
    case "Home":
      next = row.parentElement?.firstElementChild;
      break;
    case "End":
      next = row.parentElement?.lastElementChild;
      break;
    case "Enter":
    case " ":
      event.preventDefault();
      row.click();
      return;
    default:
      return;
  }
  event.preventDefault();
  if (next instanceof HTMLElement) next.focus();
}

async function copyKeyName() {
  const key = keyMenuItem.value?.key;
  keyMenu.value = null;
  if (key === undefined) return;
  try {
    await copyText(key);
    ElMessage.success(t("database.redis.keyNameCopied"));
  } catch (cause) {
    ElMessage.error(t("database.copyFailed", { message: String(cause) }));
  }
}

function formatJsonObjectOrArray(value: string) {
  const source = value.trim();
  if (!source.startsWith("{") && !source.startsWith("[")) return undefined;

  try {
    return JSON.stringify(JSON.parse(source), null, 2);
  } catch {
    return undefined;
  }
}

const canFormatEditorJson = computed(() => Boolean(formatJsonObjectOrArray(editorValue.value)));

function serialize(value: unknown) {
  if (typeof value === "string") return formatJsonObjectOrArray(value) ?? value;
  return JSON.stringify(value ?? null, null, 2);
}

function formatEditorJson() {
  const formatted = formatJsonObjectOrArray(editorValue.value);
  if (!formatted) return;
  editorValue.value = formatted;
  ElMessage.success(t("database.redis.jsonFormatted"));
}

function parseValue(type: string, value: string) {
  if (type === "string") return value;
  try {
    return JSON.parse(value);
  } catch {
    throw new Error(t("database.redis.invalidJsonValue"));
  }
}

watch(
  () => store.redisKeyInfo,
  (info) => {
    if (!info) {
      // Keep the previous key's editor (locked) while the next key loads.
      if (keyLoading.value) return;
      shownKey.value = "";
      editorType.value = "string";
      editorTtl.value = -1;
      editorValue.value = "";
      loadedValue.value = "";
      loadedTtl.value = -1;
      return;
    }
    shownKey.value = store.selectedRedisKey;
    editorType.value = info.type;
    editorTtl.value = info.ttl;
    editorValue.value = info.binary && typeof info.valueHex === "string" ? info.valueHex : serialize(info.value);
    loadedValue.value = editorValue.value;
    loadedTtl.value = info.ttl;
  },
  { immediate: true }
);

// Database switches, deletes and reconnects clear the selection: forget the locked previous key too.
watch(
  () => store.selectedRedisKey,
  (key) => {
    if (!key) shownKey.value = "";
  }
);

let searchDebounce: ReturnType<typeof setTimeout> | undefined;

async function refresh(reset = true) {
  try {
    await store.listRedisKeys(reset);
    return true;
  } catch (cause) {
    ElMessage.error(String(cause));
    return false;
  }
}

let currentSessionKey = "";
let loadedSessionKey = "";
let loadingSessionKey = "";
let sessionGeneration = 0;
watch(
  () => [store.activeWorkspaceScopeId, store.activeSessionId, store.activeSchema, store.isRedis] as const,
  ([scopeId, sessionId, schema, isRedis]) => {
    if (!isRedis || !sessionId || !schema) {
      if (currentSessionKey) sessionGeneration += 1;
      currentSessionKey = "";
      loadedSessionKey = "";
      loadingSessionKey = "";
      return;
    }
    const sessionKey = `${scopeId}:${sessionId}`;
    if (currentSessionKey !== sessionKey) {
      sessionGeneration += 1;
      currentSessionKey = sessionKey;
      loadedSessionKey = "";
      loadingSessionKey = "";
    }
    if (loadedSessionKey === sessionKey || loadingSessionKey === sessionKey) return;
    loadingSessionKey = sessionKey;
    const generation = sessionGeneration;
    void refresh(true).then((loaded) => {
      if (sessionGeneration !== generation) return;
      loadingSessionKey = "";
      if (loaded) loadedSessionKey = sessionKey;
    });
  },
  { immediate: true }
);

async function selectDatabase(value: unknown) {
  const dbIndex = Number(value);
  try {
    await store.selectRedisDatabase(dbIndex);
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}

function searchNow() {
  if (searchDebounce) clearTimeout(searchDebounce);
  searchDebounce = undefined;
  void refresh(true);
}

function scheduleSearch() {
  if (searchDebounce) clearTimeout(searchDebounce);
  searchDebounce = setTimeout(searchNow, 280);
}

async function handleAiRedisWorkspaceAction(input: Record<string, unknown>) {
  const action = typeof input.action === "string" ? input.action : "";
  switch (action) {
    case "open-key": {
      const key = typeof input.key === "string" ? input.key.trim() : "";
      if (!key) throw new Error("Redis AI action open-key requires key");
      const requestedConnectionId = typeof input.connectionId === "string" ? input.connectionId : "";
      if (!store.isRedis || store.activeConnectionId !== requestedConnectionId) {
        throw new Error("Redis AI action open-key requires the requested Redis connection to be active");
      }
      const dbIndex = typeof input.dbIndex === "number" && Number.isInteger(input.dbIndex) && input.dbIndex >= 0
        ? input.dbIndex
        : 0;
      await store.selectRedisDatabase(dbIndex);
      await loadKey(key);
      return {
        key,
        database: dbIndex,
        type: store.redisKeyInfo?.type,
        ttl: store.redisKeyInfo?.ttl,
      };
    }
    case "format-json": {
      if (!store.selectedRedisKey || keyLoading.value) throw new Error("Redis AI action format-json requires an open key");
      if (!canFormatEditorJson.value) {
        throw new Error("Redis AI action format-json requires the open value to be a JSON object or array");
      }
      formatEditorJson();
      return { key: store.selectedRedisKey, formatted: true };
    }
    default:
      throw new Error(`Unknown Redis AI workspace action: ${action || "request"}`);
  }
}

const unregisterAiRedisWorkspaceActions = registerAiWorkspaceActionHandler(
  "redis",
  handleAiRedisWorkspaceAction
);

onBeforeUnmount(() => {
  unregisterAiRedisWorkspaceActions();
  if (searchDebounce) clearTimeout(searchDebounce);
  if (keySpinnerTimer) clearTimeout(keySpinnerTimer);
});

/** Loads key into the store while the pane stays on the previous key; a newer selection supersedes it. */
async function loadKey(key: string) {
  loadingKey.value = key;
  showKeySpinner.value = false;
  if (keySpinnerTimer) clearTimeout(keySpinnerTimer);
  keySpinnerTimer = setTimeout(() => {
    if (loadingKey.value === key) showKeySpinner.value = true;
  }, 200);
  try {
    await store.getRedisKey(key);
  } finally {
    if (loadingKey.value === key) {
      loadingKey.value = "";
      showKeySpinner.value = false;
      // A failed load leaves no key info: drop the locked previous key and show the placeholder.
      if (!store.redisKeyInfo) shownKey.value = "";
    }
  }
}

async function selectKey(key: string) {
  try {
    await loadKey(key);
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}

async function save() {
  if (!store.selectedRedisKey || keyLoading.value) return;
  const valueChanged = !valueReadOnly.value && editorValue.value !== loadedValue.value;
  const ttlChanged = editorTtl.value !== loadedTtl.value;
  if (!valueChanged && !ttlChanged) {
    ElMessage.info(t("database.redis.noChanges"));
    return;
  }
  try {
    if (valueChanged) {
      await store.saveRedisKey(
        store.selectedRedisKey,
        editorType.value,
        parseValue(editorType.value, editorValue.value),
        editorTtl.value
      );
    } else {
      await store.setRedisKeyTtl(store.selectedRedisKey, editorTtl.value);
    }
    ElMessage.success(t("database.redis.saved"));
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}

async function create() {
  if (!createKey.value.trim()) return;
  try {
    await store.addRedisKey(
      createKey.value.trim(),
      createType.value,
      parseValue(createType.value, createValue.value),
      createTtl.value
    );
    createVisible.value = false;
    ElMessage.success(t("database.redis.created"));
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}

async function remove() {
  if (!store.selectedRedisKey || keyLoading.value) return;
  try {
    await store.deleteRedisKey(store.selectedRedisKey);
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}
</script>

<template>
  <div class="redis-workspace-panel">
    <aside class="redis-key-pane">
      <div class="redis-key-toolbar">
        <el-select
          :model-value="store.redisDbIndex"
          class="redis-db-select"
          popper-class="redis-db-select-popper"
          size="small"
          @change="selectDatabase"
        >
          <el-option v-for="index in 16" :key="index - 1" :label="`DB ${index - 1}`" :value="index - 1" />
        </el-select>
        <AppSearchField
          v-model="store.redisPattern"
          class="redis-key-search"
          placeholder="* / user:*"
          @input="scheduleSearch"
          @submit="searchNow"
        />
        <el-button text :icon="Refresh" :aria-label="t('common.refresh')" @click="refresh(true)" />
        <el-button text :icon="Plus" :aria-label="t('common.add')" @click="createVisible = true" />
      </div>
      <div class="redis-key-summary">{{ store.redisKeys.total }} keys</div>
      <el-scrollbar class="redis-key-list">
        <!-- Plain rows, not el-menu: with thousands of loaded keys, selecting one would update an
             ElMenuItem component per row. -->
        <div
          v-if="store.redisKeys.keys.length"
          class="redis-key-menu"
          role="listbox"
          :aria-label="t('database.redis.keyList')"
        >
          <div
            v-for="item in store.redisKeys.keys"
            :key="item.key"
            class="redis-key-row"
            :class="{ 'is-active': item.key === store.selectedRedisKey }"
            role="option"
            :aria-selected="item.key === store.selectedRedisKey"
            :tabindex="item.key === tabStopKey ? 0 : -1"
            aria-keyshortcuts="ContextMenu Shift+F10"
            @click="selectKey(item.key)"
            @focus="focusedKey = item.key"
            @contextmenu.prevent="openKeyMenu($event, item.key)"
            @keydown="handleKeyRowKeydown($event, item.key)"
          >
            <span :class="['redis-type-badge', `redis-type-badge--${item.type.toLowerCase()}`]">{{ item.type }}</span>
            <strong class="redis-key-row__name" :title="item.key">{{ item.key }}</strong>
            <small class="redis-key-row__ttl">{{ item.ttl < 0 ? t('database.redis.permanent') : `${item.ttl}s` }}</small>
          </div>
        </div>
        <el-empty v-else :description="t('database.redis.noMatchingKeys')" :image-size="56" />
      </el-scrollbar>
      <el-button v-if="hasMore" class="redis-load-more" text @click="refresh(false)">
        {{ t("database.redis.loadMore") }}
      </el-button>
      <AppContextMenu
        v-if="keyMenu"
        :open="true"
        :x="keyMenu.x"
        :y="keyMenu.y"
        :width="196"
        :aria-label="t('database.redis.keyActions')"
        @close="keyMenu = null"
      >
        <el-dropdown-item :icon="CopyDocument" @click="copyKeyName">{{ t("database.redis.copyKeyName") }}</el-dropdown-item>
        <PluginMenuItems
          menu="redis/key/context"
          :host="keyPluginMenuHost"
          :context="keyPluginMenuContext"
          @run="keyMenu = null"
        />
      </AppContextMenu>
    </aside>

    <main v-loading="showKeySpinner" class="redis-value-pane">
      <template v-if="(store.redisKeyInfo && store.selectedRedisKey) || (keyLoading && shownKey)">
        <header>
          <div>
            <span :class="['redis-type-badge', `redis-type-badge--${editorType.toLowerCase()}`]">{{ editorType }}</span>
            <strong :title="displayKey">{{ displayKey }}</strong>
          </div>
          <div class="redis-value-actions">
            <el-button
              v-if="canFormatEditorJson && !valueReadOnly"
              :disabled="keyLoading"
              text
              :icon="MagicStick"
              class="redis-format-json"
              :aria-label="t('database.redis.formatJson')"
              :title="t('database.redis.formatJson')"
              @click="formatEditorJson"
            />
            <el-popconfirm :title="t('database.redis.deleteConfirm')" @confirm="remove">
              <template #reference>
                <el-button text type="danger" :icon="Delete" :disabled="keyLoading">{{ t("common.delete") }}</el-button>
              </template>
            </el-popconfirm>
          </div>
        </header>
        <div class="redis-meta-form">
          <label>{{ t("database.redis.type") }}<el-select v-model="editorType" size="small" disabled><el-option :label="editorType" :value="editorType" /></el-select></label>
          <label>{{ t("database.redis.ttlSeconds") }}<el-input-number v-model="editorTtl" :min="-1" :disabled="keyLoading" controls-position="right" /></label>
        </div>
        <el-alert
          v-if="valueReadOnly"
          class="redis-binary-notice"
          type="warning"
          :closable="false"
          show-icon
          :title="editorType === 'string' ? t('database.redis.binaryStringNotice') : t('database.redis.binaryCollectionNotice')"
        />
        <el-scrollbar class="redis-value-scroll">
          <el-input
            v-model="editorValue"
            class="redis-value-editor"
            :readonly="valueReadOnly || keyLoading"
            type="textarea"
            :autosize="{ minRows: 12 }"
            resize="none"
            spellcheck="false"
          />
        </el-scrollbar>
        <footer><el-button type="primary" :disabled="keyLoading" @click="save">{{ t("database.redis.saveKey") }}</el-button></footer>
      </template>
      <!-- First key of the session: nothing to keep on screen, so no placeholder text while it loads. -->
      <div v-else-if="keyLoading" class="redis-value-pending" />
      <el-empty v-else :description="t('database.redis.selectHint')" />
    </main>
  </div>

  <el-dialog v-model="createVisible" :title="t('database.redis.createTitle')" width="560px" append-to-body>
    <el-form label-position="top">
      <el-form-item label="Key"><el-input v-model="createKey" autofocus /></el-form-item>
      <div class="redis-dialog-grid">
        <el-form-item :label="t('database.redis.type')">
          <el-select v-model="createType"><el-option v-for="type in ['string','hash','list','set','zset','stream']" :key="type" :label="type" :value="type" /></el-select>
        </el-form-item>
        <el-form-item label="TTL"><el-input-number v-model="createTtl" :min="-1" /></el-form-item>
      </div>
      <el-form-item :label="t('database.redis.valueJsonHint')"><el-input v-model="createValue" type="textarea" :rows="8" /></el-form-item>
    </el-form>
    <template #footer><el-button @click="createVisible = false">{{ t("common.cancel") }}</el-button><el-button type="primary" @click="create">{{ t("database.redis.create") }}</el-button></template>
  </el-dialog>
</template>
