<script setup lang="ts">
// 关联对象选择对话框：五类对象各一个标签页，列表来自共用的关联对象快照。
// 只有当前工作区的项目与 AI 对话可选；已关联的显示勾选，点击切换关联（经 store.addLink / removeLink）。
import { computed, reactive, ref } from "vue";
import { ElMessage } from "element-plus";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import { useLinkObjectSnapshot } from "@/composables/use-note-link-snapshot";
import { noteLinkKey } from "@/domain/notes/link-targets";
import type { NoteLinkInput, NoteLinkType } from "@/domain/notes/types";
import { useVueI18n } from "@/i18n/messages";
import { noteErrorMessage } from "@/services/notes-service";
import { useNotesStore } from "@/stores/notes";

interface LinkCandidate {
  key: string;
  name: string;
  detail: string;
  input: NoteLinkInput;
}

const LINK_TYPES = ["ssh", "database", "ai", "http"] as const;
type LinkType = (typeof LINK_TYPES)[number];

const props = defineProps<{ noteId: string; modelValue: boolean }>();
const emit = defineEmits<{ (e: "update:modelValue", value: boolean): void }>();
const { t } = useVueI18n();
const store = useNotesStore();
const snapshot = useLinkObjectSnapshot();

const activeType = ref<LinkType>("ssh");
const queries = reactive<Record<LinkType, string>>({ ssh: "", database: "", ai: "", http: "" });
const busyKeys = reactive(new Set<string>());

const visible = computed({
  get: () => props.modelValue,
  set: (value: boolean) => emit("update:modelValue", value),
});

const linkedKeys = computed(() => new Set((store.loadedNotes[props.noteId]?.links ?? []).map(noteLinkKey)));

function candidate(input: NoteLinkInput, detail = ""): LinkCandidate {
  return { key: noteLinkKey({ objectType: input.objectType, objectId: input.objectId, scopeId: input.scopeId ?? "" }), name: input.nameSnapshot ?? "", detail, input };
}

function fromMap(objectType: NoteLinkType, objects: Map<string, string>) {
  return [...objects].map(([objectId, name]) => candidate({ objectType, objectId, nameSnapshot: name }));
}

const candidates = computed<Record<LinkType, LinkCandidate[]>>(() => {
  const value = snapshot.value;
  const scopeName = value.workspaceNames.get(value.currentScopeId) ?? "";
  const byName = (items: LinkCandidate[]) => items.sort((a, b) => a.name.localeCompare(b.name));
  return {
    ssh: byName(fromMap("ssh", value.ssh)),
    database: byName(fromMap("database", value.database)),
    // 对话保持 store 的顺序（最近的在前）。
    ai: [...value.currentAi].map(([id, title]) =>
      candidate({
        objectType: "ai",
        objectId: id,
        nameSnapshot: title,
        scopeId: value.currentScopeId,
        scopeNameSnapshot: scopeName,
      }),
    ),
    http: byName(
      [...value.http].map(([requestId, name]) =>
        candidate({ objectType: "http", objectId: requestId, nameSnapshot: name, parentId: value.httpCollections.get(requestId) ?? "" }),
      ),
    ),
  };
});

function filtered(type: LinkType) {
  const query = queries[type].trim().toLocaleLowerCase();
  const items = candidates.value[type];
  if (!query) return items;
  return items.filter((item) => `${item.name}\n${item.detail}`.toLocaleLowerCase().includes(query));
}

function emptyText(type: LinkType) {
  if (candidates.value[type].length) return t("notes.link.noMatch");
  return snapshot.value.unavailable.has(type) ? t("notes.link.unavailable") : t(`notes.link.empty.${type}`);
}

function displayName(item: LinkCandidate) {
  return item.name || t("notes.link.untitled");
}

async function toggle(item: LinkCandidate) {
  if (busyKeys.has(item.key)) return;
  busyKeys.add(item.key);
  try {
    if (linkedKeys.value.has(item.key)) {
      await store.removeLink(props.noteId, item.input.objectType, item.input.objectId, item.input.scopeId ?? "");
    } else {
      await store.addLink(props.noteId, item.input);
    }
  } catch (error) {
    ElMessage.error(noteErrorMessage(error, t));
  } finally {
    busyKeys.delete(item.key);
  }
}
</script>

<template>
  <el-dialog v-model="visible" class="note-link-dialog" :title="t('notes.link.dialogTitle')" width="520px" append-to-body>
    <el-tabs v-model="activeType" class="note-link-dialog__tabs">
      <el-tab-pane v-for="type in LINK_TYPES" :key="type" :name="type" :label="t(`notes.linkType.${type}`)">
        <AppSearchField
          v-model="queries[type]"
          class="note-link-dialog__search"
          clearable
          :placeholder="t('notes.link.searchPlaceholder')"
          :aria-label="t('notes.link.searchLabel', { type: t(`notes.linkType.${type}`) })"
          :clear-label="t('notes.link.clearSearch')"
        />
        <p v-if="type === 'ai'" class="note-link-dialog__hint">{{ t("notes.link.currentWorkspaceOnly") }}</p>
        <el-scrollbar class="note-link-dialog__scroll" max-height="320px">
          <ul v-if="filtered(type).length" class="note-link-dialog__list" role="list">
            <li v-for="item in filtered(type)" :key="item.key">
              <el-checkbox
                class="note-link-dialog__item"
                :class="{ 'is-linked': linkedKeys.has(item.key) }"
                :model-value="linkedKeys.has(item.key)"
                :disabled="busyKeys.has(item.key)"
                @change="toggle(item)"
              >
                <span class="note-link-dialog__name" :title="displayName(item)">{{ displayName(item) }}</span>
                <span v-if="item.detail" class="note-link-dialog__detail" :title="item.detail">{{ item.detail }}</span>
              </el-checkbox>
            </li>
          </ul>
          <el-empty
            v-else
            :image-size="56"
            :description="emptyText(type)"
          />
        </el-scrollbar>
      </el-tab-pane>
    </el-tabs>
  </el-dialog>
</template>
