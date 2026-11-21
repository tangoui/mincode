<script setup lang="ts">
// 笔记页：左栏 NotesSidebar（可拖动宽度、可折叠），右栏共用编辑区 NoteEditor。
// 当前笔记取自 store.selectedNoteId，关联笔记抽屉等外部选中在 KeepAlive 缓存期间也能生效。
// 笔记仅桌面端可用：浏览器预览只显示说明，不调用任何命令。
import { computed, onMounted, ref, watch } from "vue";
import { ArrowDown, EditPen, Upload } from "@element-plus/icons-vue";
import NoteEditor from "@/components/notes/NoteEditor.vue";
import NotesSidebar from "@/components/notes/NotesSidebar.vue";
import AppSplitPane from "@/components/ui/AppSplitPane.vue";
import { useToggleSidebarCommand } from "@/composables/use-active-view-commands";
import { useResizablePane } from "@/composables/use-resizable-pane";
import { useVueI18n } from "@/i18n/messages";
import { noteErrorMessage } from "@/services/notes-service";
import { isDesktopRuntime } from "@/services/desktop-bridge";
import { useNotesStore } from "@/stores/notes";

defineOptions({ name: "NotesWorkspaceView" });

// 笔记是全局资源，左栏布局同样全局，不走工作区作用域持久化。
const SIDEBAR_WIDTH_KEY = "notes.sidebarWidth";
const SIDEBAR_COLLAPSED_KEY = "notes.sidebarCollapsed";
const SIDEBAR_MIN = 200;
const SIDEBAR_MAX = 420;

const { t } = useVueI18n();
const store = useNotesStore();
const desktop = isDesktopRuntime();

const sidebarRef = ref<InstanceType<typeof NotesSidebar> | null>(null);
const loading = ref(false);
const loadError = ref("");

const {
  size: sidebarWidth,
  collapsed: sidebarCollapsed,
  hydrate: hydrateSidebarLayout,
  persist: persistSidebarLayout,
} = useResizablePane({
  sizeKey: SIDEBAR_WIDTH_KEY,
  collapsedKey: SIDEBAR_COLLAPSED_KEY,
  defaultSize: 250,
  minSize: SIDEBAR_MIN,
  maxSize: SIDEBAR_MAX,
});

function persistSidebarWidth(value: number) {
  sidebarWidth.value = value;
  void persistSidebarLayout();
}

function updateSidebarCollapsed(value: boolean) {
  sidebarCollapsed.value = value;
  void persistSidebarLayout();
}
useToggleSidebarCommand(() => sidebarCollapsed.value, updateSidebarCollapsed);

const selectedId = computed(() => store.selectedNoteId);
const hasNotes = computed(() => (store.tree?.notes.length ?? 0) > 0);

async function loadTree() {
  loading.value = true;
  try {
    await store.loadTree();
    loadError.value = "";
  } catch (error) {
    loadError.value = noteErrorMessage(error, t);
  } finally {
    loading.value = false;
  }
}

// 记住的或外部选中的笔记已不存在（被删除）时清除选中，右栏回到空状态。
watch(
  [() => store.tree?.notes, selectedId],
  ([notes, id]) => {
    if (notes && id && !notes.some((note) => note.id === id)) store.selectNote("");
  },
);

onMounted(() => {
  if (!desktop) return;
  void hydrateSidebarLayout();
  void loadTree();
});
</script>

<template>
  <section class="notes-workspace">
    <div v-if="!desktop" class="notes-workspace__state">
      <el-empty :image-size="72" :description="t('notes.desktopOnly')" />
    </div>
    <AppSplitPane
      v-else
      :model-value="sidebarWidth"
      :collapsed="sidebarCollapsed"
      side="first"
      :min="SIDEBAR_MIN"
      :max="SIDEBAR_MAX"
      :second-min="360"
      collapsible
      :collapse-label="t('notes.editor.collapseSidebar')"
      :expand-label="t('notes.editor.expandSidebar')"
      :aria-label="t('notes.page.resizeSidebar')"
      @resize-end="persistSidebarWidth"
      @update:collapsed="updateSidebarCollapsed"
    >
      <template #first>
        <NotesSidebar v-if="store.tree" ref="sidebarRef" :selected-id="selectedId" @select="store.selectNote" />
        <div v-else class="notes-workspace__sidebar-state">
          <template v-if="loadError">
            <p class="notes-workspace__error">{{ t("notes.sidebar.loadFailed", { message: loadError }) }}</p>
            <el-button size="small" :loading="loading" @click="loadTree">{{ t("notes.editor.retry") }}</el-button>
          </template>
          <span v-else>{{ t("notes.sidebar.loading") }}</span>
        </div>
      </template>
      <template #second>
        <NoteEditor
          v-if="selectedId"
          :note-id="selectedId"
          :sidebar-collapsed="sidebarCollapsed"
          @toggle-sidebar="updateSidebarCollapsed(!sidebarCollapsed)"
        />
        <div v-else class="notes-workspace__state">
          <el-empty :image-size="72" :description="t(hasNotes ? 'notes.page.noSelection' : 'notes.page.empty')">
            <div v-if="store.tree" class="notes-workspace__actions">
              <el-button type="primary" :icon="EditPen" @click="sidebarRef?.createNote()">{{ t("notes.sidebar.newNote") }}</el-button>
              <el-dropdown trigger="click" @command="(command: string) => command === 'folder' ? sidebarRef?.importFolder() : sidebarRef?.importFiles()">
                <el-button :icon="Upload">
                  {{ t("notes.page.import") }}
                  <el-icon class="el-icon--right" aria-hidden="true"><ArrowDown /></el-icon>
                </el-button>
                <template #dropdown>
                  <el-dropdown-menu>
                    <el-dropdown-item command="files">{{ t("notes.sidebar.importFiles") }}</el-dropdown-item>
                    <el-dropdown-item command="folder">{{ t("notes.sidebar.importFolder") }}</el-dropdown-item>
                  </el-dropdown-menu>
                </template>
              </el-dropdown>
            </div>
          </el-empty>
        </div>
      </template>
    </AppSplitPane>
  </section>
</template>
