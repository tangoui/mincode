// 全局「关联笔记」抽屉与各页面对象角标的计数。
// 这个 store 会被大页面（SSH / 项目 / 数据库 / AI / HTTP）静态引用，必须保持很小：
// 笔记 store（及其保存逻辑）与笔记服务只在真正打开抽屉或取计数时动态 import。
import { ref, watch, type Ref } from "vue";
import { acceptHMRUpdate, defineStore, getActivePinia, type Pinia } from "pinia";
import { ElMessage } from "element-plus";
import type { NoteLinkInput, NoteLinkType, NoteSummary } from "@/domain/notes/types";
import { useVueI18n } from "@/i18n/messages";
import { isPopupWindow } from "@/services/popup-tab";
import { isDesktopRuntime } from "@/services/desktop-bridge";

/** 抽屉当前对象：与 notes_links_add 的参数一致，打开时补齐为空串。 */
export type LinkedNotesTarget = Required<NoteLinkInput>;

export interface OpenLinkedNotesOptions {
  /** 新建一篇笔记、关联到对象并在抽屉中选中。 */
  createNew?: boolean;
}

const NOTE_LINK_TYPES: readonly NoteLinkType[] = ["ssh", "project", "database", "ai", "http"];

type NotesStore = ReturnType<typeof import("@/stores/notes").useNotesStore>;
type NotesService = typeof import("@/services/notes-service");

let servicePromise: Promise<NotesService> | undefined;
/** 笔记服务只在第一次需要时加载，之后复用同一个模块。 */
function loadService() {
  servicePromise ??= import("@/services/notes-service");
  return servicePromise;
}

function emptyByType<T>(create: () => T): Record<NoteLinkType, T> {
  return Object.fromEntries(NOTE_LINK_TYPES.map((type) => [type, create()])) as Record<NoteLinkType, T>;
}

function normalizeTarget(input: NoteLinkInput): LinkedNotesTarget {
  return {
    objectType: input.objectType,
    objectId: input.objectId,
    parentId: input.parentId ?? "",
    scopeId: input.scopeId ?? "",
    nameSnapshot: input.nameSnapshot ?? "",
    scopeNameSnapshot: input.scopeNameSnapshot ?? "",
  };
}

export const useLinkedNotesDrawerStore = defineStore("linked-notes-drawer", () => {
  const { t } = useVueI18n();
  // 动态 import 之后 active pinia 可能已变（测试中尤其如此），始终用创建本 store 的那个。
  const pinia = getActivePinia() as Pinia;

  const target = ref<LinkedNotesTarget | null>(null);
  const visible = ref(false);
  /** 首次 open 之后才在 App 中渲染抽屉组件（其 chunk 含编辑器）。 */
  const rendered = ref(false);
  const notes = ref<NoteSummary[]>([]);
  const activeNoteId = ref("");
  const listLoading = ref(false);
  const listError = ref("");
  const creating = ref(false);

  let notesStore: NotesStore | undefined;
  let openRequest = 0;
  let listRequest = 0;

  async function loadNotes(): Promise<{ store: NotesStore; service: NotesService }> {
    const { useNotesStore } = await import("@/stores/notes");
    const service = await loadService();
    notesStore ??= useNotesStore(pinia);
    return { store: notesStore, service };
  }

  async function errorText(error: unknown) {
    const { noteErrorMessage } = await loadService();
    return noteErrorMessage(error, t);
  }

  // ---- 对象角标计数 ----

  const countRefs = emptyByType(() => ref<Record<string, number>>({}));
  /** 每类对象当前被角标 / 菜单使用的 id 及引用数。 */
  const tracked = emptyByType(() => new Map<string, number>());
  /** 已取得计数（含 0）的 id。 */
  const known = emptyByType(() => new Set<string>());
  const queued = emptyByType(() => new Set<string>());
  const generations = emptyByType(() => 0);
  const scheduled = new Set<NoteLinkType>();

  function counts(type: NoteLinkType): Ref<Record<string, number>> {
    return countRefs[type];
  }

  /** 对象的关联笔记数；未取得时为 0。 */
  function countOf(type: NoteLinkType, id: string): number {
    return countRefs[type].value[id] ?? 0;
  }

  async function fetchCounts(type: NoteLinkType) {
    scheduled.delete(type);
    const ids = [...queued[type]];
    queued[type].clear();
    if (!ids.length) return;
    const generation = generations[type];
    try {
      const { countNoteLinks } = await loadService();
      const result = await countNoteLinks(type, ids);
      if (generation !== generations[type]) return;
      const next = { ...countRefs[type].value };
      for (const id of ids) {
        known[type].add(id);
        // 后端省略计数为 0 的对象。
        const count = result[id] ?? 0;
        if (count > 0) next[id] = count;
        else delete next[id];
      }
      countRefs[type].value = next;
    } catch {
      // 计数只是提示；失败时保持原样，下次失效或重新使用时再取。
    }
  }

  /** 把尚未取得计数的 id 排进同一批请求；浏览器预览没有笔记后端，不请求。 */
  function ensureCounts(type: NoteLinkType, ids: readonly string[]) {
    if (!isDesktopRuntime()) return;
    for (const id of ids) if (id && !known[type].has(id)) queued[type].add(id);
    if (!queued[type].size || scheduled.has(type)) return;
    scheduled.add(type);
    queueMicrotask(() => void fetchCounts(type));
  }

  /** 角标挂载时登记 id，返回注销函数；同一渲染周期内登记的 id 合并为一次请求。 */
  function track(type: NoteLinkType, id: string): () => void {
    const map = tracked[type];
    map.set(id, (map.get(id) ?? 0) + 1);
    ensureCounts(type, [id]);
    let active = true;
    return () => {
      if (!active) return;
      active = false;
      const rest = (map.get(id) ?? 1) - 1;
      if (rest > 0) map.set(id, rest);
      else map.delete(id);
    };
  }

  /** 关联可能已变：丢弃全部缓存，重取正在显示的对象的计数。 */
  function invalidateCounts() {
    for (const type of NOTE_LINK_TYPES) {
      generations[type]++;
      known[type].clear();
      queued[type].clear();
      const ids = [...tracked[type].keys()];
      // 不再显示的对象直接丢弃旧计数；仍显示的保留到新结果到达，避免闪烁。
      const current = countRefs[type].value;
      countRefs[type].value = Object.fromEntries(ids.filter((id) => id in current).map((id) => [id, current[id]!]));
      ensureCounts(type, ids);
    }
  }

  // 笔记 store 由本 store 或笔记页按需创建；不 import 它，而是从 pinia 状态读它的 linksVersion。
  // 它尚未创建时不可能有本地关联改动，计数不会因此过期。
  watch(
    () => (pinia.state.value.notes as { linksVersion?: unknown } | undefined)?.linksVersion,
    (value, previous) => {
      if (typeof value === "number" && typeof previous === "number") invalidateCounts();
    },
  );

  // ---- 抽屉 ----

  async function reloadList() {
    const current = target.value;
    if (!current) return;
    const request = ++listRequest;
    listLoading.value = true;
    listError.value = "";
    try {
      const { service } = await loadNotes();
      // AI 对话可能已移动到别的工作区：按对话 id 列出（scope 不限），记录在旧 scope 下的关联由 adoptAiLinks 迁移。
      const list = await service.listNotesForObject(current.objectType, current.objectId, undefined);
      if (request !== listRequest) return;
      notes.value = list;
      if (!list.some((item) => item.id === activeNoteId.value)) activeNoteId.value = list[0]?.id ?? "";
    } catch (error) {
      if (request === listRequest) listError.value = await errorText(error);
    } finally {
      if (request === listRequest) listLoading.value = false;
    }
  }

  /**
   * 对话移动过工作区后，列表中的笔记可能把它记录在旧 scope 下：改记到当前 scope 与快照，
   * 让计数、解析和跳转都按当前工作区。尽力而为，失败时保留旧关联。
   */
  async function adoptAiLinks(request: number) {
    const current = target.value;
    if (!current || current.objectType !== "ai" || !current.scopeId) return;
    const { store } = await loadNotes();
    for (const item of [...notes.value]) {
      if (request !== openRequest) return;
      try {
        const note = await store.openNote(item.id);
        const stale = note.links.some((link) =>
          link.objectType === "ai" && link.objectId === current.objectId && link.scopeId !== current.scopeId);
        if (stale && request === openRequest) await store.addLink(item.id, current);
      } catch {
        // 迁移只是整理；旧关联仍能按对话 id 找到。
      }
    }
  }

  /** 为当前对象新建笔记并关联、选中。 */
  async function createLinkedNote() {
    const current = target.value;
    if (!current || creating.value) return;
    const request = openRequest;
    creating.value = true;
    try {
      const { store } = await loadNotes();
      const note = await store.createNote({});
      try {
        await store.addLink(note.id, current);
      } catch (error) {
        // 关联失败时新笔记不会出现在任何对象下：尽力删掉，避免留下看不到的空笔记。
        await store.deleteNote(note.id).catch(() => undefined);
        throw error;
      }
      if (request !== openRequest) return;
      activeNoteId.value = note.id;
      await reloadList();
    } catch (error) {
      ElMessage.error(await errorText(error));
    } finally {
      creating.value = false;
    }
  }

  function selectNote(id: string) {
    // NoteEditor 切换笔记时会 flush 旧笔记。
    activeNoteId.value = id;
  }

  async function open(next: NoteLinkInput, options: OpenLinkedNotesOptions = {}) {
    // 弹出窗口不渲染抽屉，且笔记草稿只在主窗口的 store 中保存；这里新建或打开都会不可见或互相覆盖。
    if (isPopupWindow()) {
      ElMessage.info(t("notes.drawer.mainWindowOnly"));
      return;
    }
    if (notesStore && visible.value) await notesStore.flushAll();
    const request = ++openRequest;
    listRequest++;
    target.value = normalizeTarget(next);
    notes.value = [];
    activeNoteId.value = "";
    listError.value = "";
    listLoading.value = true;
    rendered.value = true;
    visible.value = true;
    if (options.createNew) {
      await createLinkedNote();
      if (request !== openRequest) return;
      if (!activeNoteId.value) await reloadList();
      return;
    }
    await reloadList();
    await adoptAiLinks(request);
  }

  /**
   * 先保存所有未保存的编辑，再关闭。抽屉中的笔记仍有保存错误时不关闭并提示，
   * 避免用户在看不到错误的情况下离开（文本仍在内存中，可重试）。返回是否已关闭。
   */
  async function close(): Promise<boolean> {
    if (notesStore) {
      await notesStore.flushAll();
      const ids = new Set([activeNoteId.value, ...notes.value.map((item) => item.id)]);
      const failed = [...ids].map((id) => notesStore!.saveErrors[id]).find(Boolean);
      if (failed) {
        ElMessage.error(t("notes.editor.saveFailed", { message: failed }));
        return false;
      }
    }
    visible.value = false;
    invalidateCounts();
    return true;
  }

  return {
    target,
    visible,
    rendered,
    notes,
    activeNoteId,
    listLoading,
    listError,
    creating,
    counts,
    countOf,
    track,
    ensureCounts,
    invalidateCounts,
    reloadList,
    createLinkedNote,
    selectNote,
    open,
    close,
  };
});

/** 关联笔记抽屉与角标计数（全局单例）。 */
export function useLinkedNotesDrawer() {
  return useLinkedNotesDrawerStore();
}

if (import.meta.hot) import.meta.hot.accept(acceptHMRUpdate(useLinkedNotesDrawerStore, import.meta.hot));
