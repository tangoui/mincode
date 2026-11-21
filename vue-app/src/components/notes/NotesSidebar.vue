<script setup lang="ts">
// 笔记页左栏：工具条（新建、导入、导出、全部折叠）、搜索、分段视图（全部 / 收藏 / 标签 / 关联）。
// 搜索结果的标题与片段逐段渲染为文本与 <mark>，从不使用 v-html。
import { computed, h, onBeforeUnmount, ref, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { Download, EditPen, Fold, FolderAdd, FolderOpened, Upload } from "@element-plus/icons-vue";
import AppSearchField from "@/components/ui/AppSearchField.vue";
import NotesFolderTree from "@/components/notes/NotesFolderTree.vue";
import { useLinkObjectSnapshot } from "@/composables/use-note-link-snapshot";
import { resolveLink, type LinkStatus } from "@/domain/notes/link-targets";
import type { NotesExportTarget } from "@/domain/notes/folder-tree";
import { highlightSegments } from "@/domain/notes/search-highlight";
import type { NoteImportReport, NoteLinkEntry, NoteLinkType, NoteSearchHit, NoteSummary } from "@/domain/notes/types";
import { useVueI18n } from "@/i18n/messages";
import {
  exportNotes,
  importNoteFiles,
  importNoteFolder,
  listAllNoteLinks,
  noteErrorMessage,
  noteImportSkipReasonKey,
  searchNotes,
} from "@/services/notes-service";
import { chooseNativeDirectory, chooseNativeFiles } from "@/services/desktop-bridge";
import { useNotesStore } from "@/stores/notes";

type Segment = "all" | "favorites" | "tags" | "links";

const SEARCH_DEBOUNCE_MS = 200;
const SEARCH_LIMIT = 50;
const SEGMENTS: readonly Segment[] = ["all", "favorites", "tags", "links"];
const LINK_TYPES: readonly NoteLinkType[] = ["ssh", "project", "database", "ai", "http"];

const props = defineProps<{ selectedId: string }>();
const emit = defineEmits<{ (e: "select", noteId: string): void }>();

const { t } = useVueI18n();
const store = useNotesStore();

const segment = ref<Segment>("all");
const segmentOptions = computed(() => SEGMENTS.map((value) => ({ value, label: t(`notes.sidebar.segment.${value}`) })));
const treeRef = ref<InstanceType<typeof NotesFolderTree> | null>(null);
const activeFolderId = ref("");
const busy = ref(false);

const notes = computed(() => store.tree?.notes ?? []);
const notesById = computed(() => new Map(notes.value.map((note) => [note.id, note])));

function titleOf(note: Pick<NoteSummary, "title">) {
  return note.title || t("notes.editor.untitled");
}

/** 搜索结果优先显示树中的最新标题（含未保存的编辑）；笔记已不在树中时才用命中时的快照。 */
function hitTitle(hit: NoteSearchHit) {
  return titleOf(notesById.value.get(hit.note.id) ?? hit.note);
}

function select(id: string) {
  activeFolderId.value = "";
  emit("select", id);
}

// 当前文件夹失效（被删除）时不再作为目标。
const targetFolderId = computed(() => {
  if (activeFolderId.value && store.tree?.folders.some((folder) => folder.id === activeFolderId.value)) return activeFolderId.value;
  return notesById.value.get(props.selectedId)?.folderId ?? "";
});

const exportTarget = computed<NotesExportTarget | null>(() => {
  if (activeFolderId.value && store.tree?.folders.some((folder) => folder.id === activeFolderId.value)) return { folderId: activeFolderId.value };
  return props.selectedId && notesById.value.has(props.selectedId) ? { noteIds: [props.selectedId] } : null;
});

watch(
  () => props.selectedId,
  (id) => {
    if (id) activeFolderId.value = "";
  },
);

// ---- 搜索 ----

const query = ref("");
const results = ref<NoteSearchHit[] | null>(null);
const searching = ref(false);
const searchError = ref("");
let searchTimer: ReturnType<typeof setTimeout> | undefined;
let searchRequest = 0;

async function runSearch(text: string) {
  const request = ++searchRequest;
  searching.value = true;
  try {
    const hits = await searchNotes(text, SEARCH_LIMIT);
    if (request !== searchRequest) return;
    results.value = hits;
    searchError.value = "";
  } catch (error) {
    if (request !== searchRequest) return;
    results.value = [];
    searchError.value = noteErrorMessage(error, t);
  } finally {
    if (request === searchRequest) searching.value = false;
  }
}

watch(query, (value) => {
  clearTimeout(searchTimer);
  const text = value.trim();
  if (!text) {
    searchRequest++;
    results.value = null;
    searching.value = false;
    searchError.value = "";
    return;
  }
  searchTimer = setTimeout(() => void runSearch(text), SEARCH_DEBOUNCE_MS);
});
onBeforeUnmount(() => clearTimeout(searchTimer));

const searchActive = computed(() => query.value.trim() !== "");
const highlightQuery = computed(() => query.value.trim());

// ---- 收藏 / 标签 ----

const favorites = computed(() => notes.value.filter((note) => note.favorite).sort((a, b) => b.updatedAt - a.updatedAt));

const tagGroups = computed(() => {
  const groups = new Map<string, NoteSummary[]>();
  for (const note of notes.value) {
    for (const tag of note.tags) {
      const list = groups.get(tag) ?? [];
      list.push(note);
      groups.set(tag, list);
    }
  }
  return [...groups.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([tag, list]) => ({ tag, notes: list }));
});
const openTags = ref<string[]>([]);

// ---- 关联 ----

interface LinkObjectGroup {
  key: string;
  name: string;
  /** 「（其他工作区：X）」，仅对象只在其他工作区时非空。 */
  suffix: string;
  status: LinkStatus;
  notes: { id: string; title: string }[];
}

const linkSnapshot = useLinkObjectSnapshot();

const linkEntries = ref<NoteLinkEntry[]>([]);
const linksLoading = ref(false);
const linksError = ref("");
let linksRequest = 0;

async function loadLinks() {
  const request = ++linksRequest;
  linksLoading.value = true;
  try {
    const entries = await listAllNoteLinks();
    if (request !== linksRequest) return;
    linkEntries.value = entries;
    linksError.value = "";
  } catch (error) {
    if (request === linksRequest) linksError.value = noteErrorMessage(error, t);
  } finally {
    if (request === linksRequest) linksLoading.value = false;
  }
}

watch(
  [segment, () => store.linksVersion],
  ([value]) => {
    if (value === "links") void loadLinks();
  },
);

const linkGroups = computed(() =>
  LINK_TYPES.map((type) => {
    const objects = new Map<string, LinkObjectGroup>();
    for (const entry of linkEntries.value) {
      const { link } = entry;
      if (link.objectType !== type) continue;
      const key = `${link.objectId}\u0000${link.scopeId}`;
      let group = objects.get(key);
      if (!group) {
        const resolved = resolveLink(link, linkSnapshot.value);
        const status = resolved.status;
        group = {
          key,
          name: resolved.name || (link.objectType === "ai" ? t("notes.link.untitled") : link.objectId),
          suffix: resolved.status === "otherWorkspace" ? t("notes.link.otherWorkspaceSuffix", { name: resolved.workspaceName ?? "" }) : "",
          status,
          notes: [],
        };
      }
      const live = notesById.value.get(entry.noteId);
      group.notes.push({ id: entry.noteId, title: live ? titleOf(live) : entry.noteTitle || t("notes.editor.untitled") });
      objects.set(key, group);
    }
    return { type, objects: [...objects.values()].sort((a, b) => a.name.localeCompare(b.name)) };
  }).filter((group) => group.objects.length > 0),
);
const openLinkTypes = ref<string[]>([...LINK_TYPES]);

// ---- 新建 / 导入 / 导出 ----

async function guarded(action: () => Promise<void>) {
  if (busy.value) return;
  busy.value = true;
  try {
    await action();
  } catch (error) {
    ElMessage.error(noteErrorMessage(error, t));
  } finally {
    busy.value = false;
  }
}

function createNote(folderId = targetFolderId.value) {
  return guarded(async () => {
    const note = await store.createNote({ folderId });
    select(note.id);
  });
}

function createFolder() {
  return guarded(async () => {
    let name: string;
    try {
      const result = await ElMessageBox.prompt(t("notes.tree.folderName"), t("notes.sidebar.newFolder"), {
        confirmButtonText: t("common.create"),
        cancelButtonText: t("common.cancel"),
        inputValidator: (input: string) => (input.trim() ? true : t("notes.tree.nameRequired")),
      });
      name = result.value.trim();
    } catch (error) {
      if (error === "cancel" || error === "close") return;
      throw error;
    }
    await store.createFolder(name, targetFolderId.value);
  });
}

function showImportReport(report: NoteImportReport) {
  ElMessage.success(t("notes.import.done", { notes: report.notes, assets: report.assets }));
  if (report.truncated) ElMessage.warning(t("notes.import.truncated"));
  if (report.skipped.length === 0 && report.skippedTotal === 0) return;
  const total = Math.max(report.skippedTotal, report.skipped.length);
  const content = h("div", { class: "notes-import-report" }, [
    h(
      "ul",
      { class: "notes-import-report__list" },
      report.skipped.map((item) =>
        h("li", { class: "notes-import-report__item" }, [
          h("span", { class: "notes-import-report__path" }, item.path),
          h("span", { class: "notes-import-report__reason" }, t(noteImportSkipReasonKey(item.reason))),
        ]),
      ),
    ),
    total > report.skipped.length
      ? h("p", { class: "notes-import-report__more" }, t("notes.import.skippedMore", { total, shown: report.skipped.length }))
      : null,
  ]);
  void ElMessageBox.alert(content, t("notes.import.skippedTitle"), {
    confirmButtonText: t("common.close"),
    customClass: "notes-import-report__box",
  }).catch(() => undefined);
}

function importFiles() {
  return guarded(async () => {
    const paths = await chooseNativeFiles(t("notes.import.chooseFiles"), [".md", ".markdown"]);
    if (paths.length === 0) return;
    const report = await importNoteFiles(paths, targetFolderId.value);
    await store.loadTree();
    showImportReport(report);
  });
}

function importFolder() {
  return guarded(async () => {
    const path = await chooseNativeDirectory(t("notes.import.chooseFolder"));
    if (!path) return;
    const report = await importNoteFolder(path, targetFolderId.value);
    await store.loadTree();
    showImportReport(report);
  });
}

function exportNotesTo(target: NotesExportTarget | null) {
  if (!target) return;
  return guarded(async () => {
    const destDir = await chooseNativeDirectory(t("notes.export.chooseDir"));
    if (!destDir) return;
    // 先落盘未保存的编辑，导出内容与编辑区一致。
    await store.flushAll();
    const result = await exportNotes({ ...target, destDir });
    ElMessage.success(t("notes.export.done", { count: result.written.length }));
    if (result.missingAssets.length > 0) ElMessage.warning(t("notes.export.missingAssets", { count: result.missingAssets.length }));
  });
}

function collapseAll() {
  treeRef.value?.collapseAll();
  openTags.value = [];
  openLinkTypes.value = [];
}

defineExpose({ createNote: () => createNote(), importFiles, importFolder });
</script>

<template>
  <aside class="notes-sidebar" :aria-label="t('notes.sidebar.label')">
    <div class="notes-sidebar__toolbar" role="toolbar" :aria-label="t('notes.sidebar.toolbarLabel')">
      <el-button
        text
        size="small"
        :icon="EditPen"
        :disabled="busy"
        :title="t('notes.sidebar.newNote')"
        :aria-label="t('notes.sidebar.newNote')"
        @click="createNote()"
      />
      <el-button
        text
        size="small"
        :icon="FolderAdd"
        :disabled="busy"
        :title="t('notes.sidebar.newFolder')"
        :aria-label="t('notes.sidebar.newFolder')"
        @click="createFolder"
      />
      <el-button
        text
        size="small"
        :icon="Upload"
        :disabled="busy"
        :title="t('notes.sidebar.importFiles')"
        :aria-label="t('notes.sidebar.importFiles')"
        @click="importFiles"
      />
      <el-button
        text
        size="small"
        :icon="FolderOpened"
        :disabled="busy"
        :title="t('notes.sidebar.importFolder')"
        :aria-label="t('notes.sidebar.importFolder')"
        @click="importFolder"
      />
      <el-button
        text
        size="small"
        :icon="Download"
        :disabled="busy || !exportTarget"
        :title="exportTarget ? t('notes.sidebar.export') : t('notes.sidebar.exportHint')"
        :aria-label="t('notes.sidebar.export')"
        @click="exportNotesTo(exportTarget)"
      />
      <el-button
        text
        size="small"
        :icon="Fold"
        :title="t('notes.sidebar.collapseAll')"
        :aria-label="t('notes.sidebar.collapseAll')"
        @click="collapseAll"
      />
    </div>

    <div class="notes-sidebar__search">
      <AppSearchField
        v-model="query"
        clearable
        :placeholder="t('notes.sidebar.searchPlaceholder')"
        :aria-label="t('notes.sidebar.searchPlaceholder')"
        :clear-label="t('notes.sidebar.clearSearch')"
      />
    </div>

    <el-segmented
      v-model="segment"
      class="notes-sidebar__segments"
      size="small"
      block
      :options="segmentOptions"
      :aria-label="t('notes.sidebar.viewLabel')"
    />

    <div class="notes-sidebar__content">
      <div v-if="searchActive" class="notes-sidebar__results" aria-live="polite">
        <p v-if="searchError" class="notes-sidebar__state notes-sidebar__state--error">{{ t("notes.sidebar.searchFailed", { message: searchError }) }}</p>
        <p v-else-if="searching && !results" class="notes-sidebar__state">{{ t("notes.sidebar.searching") }}</p>
        <p v-else-if="results && results.length === 0" class="notes-sidebar__state">{{ t("notes.sidebar.noResults") }}</p>
        <button
          v-for="hit in results ?? []"
          :key="hit.note.id"
          type="button"
          class="notes-sidebar__item notes-sidebar__item--hit"
          :class="{ 'is-active': hit.note.id === selectedId }"
          @click="select(hit.note.id)"
        >
          <span class="notes-sidebar__item-title">
            <template v-for="(part, index) in highlightSegments(hitTitle(hit), highlightQuery)" :key="index">
              <mark v-if="part.match">{{ part.text }}</mark>
              <template v-else>{{ part.text }}</template>
            </template>
          </span>
          <span v-if="hit.snippet" class="notes-sidebar__snippet">
            <template v-for="(part, index) in highlightSegments(hit.snippet, highlightQuery)" :key="index">
              <mark v-if="part.match">{{ part.text }}</mark>
              <template v-else>{{ part.text }}</template>
            </template>
          </span>
        </button>
      </div>

      <template v-else-if="segment === 'all'">
        <NotesFolderTree
          ref="treeRef"
          :selected-id="selectedId"
          :active-folder-id="activeFolderId"
          @select="select"
          @activate-folder="activeFolderId = $event"
          @create-note="createNote"
          @export="exportNotesTo"
        />
        <p v-if="notes.length === 0 && !store.tree?.folders.length" class="notes-sidebar__state">{{ t("notes.sidebar.empty") }}</p>
      </template>

      <div v-else-if="segment === 'favorites'" class="notes-sidebar__list">
        <p v-if="favorites.length === 0" class="notes-sidebar__state">{{ t("notes.sidebar.noFavorites") }}</p>
        <button
          v-for="note in favorites"
          :key="note.id"
          type="button"
          class="notes-sidebar__item"
          :class="{ 'is-active': note.id === selectedId }"
          @click="select(note.id)"
        >
          <span class="notes-sidebar__item-title">{{ titleOf(note) }}</span>
        </button>
      </div>

      <template v-else-if="segment === 'tags'">
        <p v-if="tagGroups.length === 0" class="notes-sidebar__state">{{ t("notes.sidebar.noTags") }}</p>
        <el-collapse v-else v-model="openTags" class="notes-sidebar__groups">
          <el-collapse-item v-for="group in tagGroups" :key="group.tag" :name="group.tag">
            <template #title>
              <span class="notes-sidebar__group-title">{{ group.tag }}</span>
              <span class="notes-sidebar__group-count">{{ group.notes.length }}</span>
            </template>
            <button
              v-for="note in group.notes"
              :key="note.id"
              type="button"
              class="notes-sidebar__item"
              :class="{ 'is-active': note.id === selectedId }"
              @click="select(note.id)"
            >
              <span class="notes-sidebar__item-title">{{ titleOf(note) }}</span>
            </button>
          </el-collapse-item>
        </el-collapse>
      </template>

      <template v-else>
        <p v-if="linksError" class="notes-sidebar__state notes-sidebar__state--error">{{ t("notes.sidebar.loadFailed", { message: linksError }) }}</p>
        <p v-else-if="linksLoading && linkGroups.length === 0" class="notes-sidebar__state">{{ t("notes.sidebar.loading") }}</p>
        <p v-else-if="linkGroups.length === 0" class="notes-sidebar__state">{{ t("notes.sidebar.noLinks") }}</p>
        <el-collapse v-else v-model="openLinkTypes" class="notes-sidebar__groups">
          <el-collapse-item v-for="group in linkGroups" :key="group.type" :name="group.type">
            <template #title>
              <span class="notes-sidebar__group-title">{{ t(`notes.linkType.${group.type}`) }}</span>
              <span class="notes-sidebar__group-count">{{ group.objects.length }}</span>
            </template>
            <div v-for="object in group.objects" :key="object.key" class="notes-sidebar__link-object">
              <div class="notes-sidebar__link-name" :class="`is-${object.status}`" :title="object.status === 'unknown' ? `${object.name}${object.suffix}\n${t('notes.link.unknownHint')}` : `${object.name}${object.suffix}`">
                <span class="notes-sidebar__link-label">{{ object.name }}</span><span v-if="object.suffix" class="notes-sidebar__link-scope">{{ object.suffix }}</span>
                <span v-if="object.status === 'deleted'" class="notes-sidebar__link-deleted">{{ t("notes.link.deleted") }}</span>
                <span v-else-if="object.status === 'unknown'" class="notes-sidebar__link-unknown">{{ t("notes.link.unknown") }}</span>
              </div>
              <button
                v-for="note in object.notes"
                :key="note.id"
                type="button"
                class="notes-sidebar__item notes-sidebar__item--nested"
                :class="{ 'is-active': note.id === selectedId }"
                @click="select(note.id)"
              >
                <span class="notes-sidebar__item-title">{{ note.title }}</span>
              </button>
            </div>
          </el-collapse-item>
        </el-collapse>
      </template>
    </div>
  </aside>
</template>
