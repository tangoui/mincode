import { onScopeDispose, ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import type {
  CreateNoteInput,
  Note,
  NoteFolder,
  NoteLink,
  NoteLinkInput,
  NoteLinkType,
  NoteSummary,
  NoteTree,
} from "@/domain/notes/types";
import { useVueI18n } from "@/i18n/messages";
import * as notesService from "@/services/notes-service";
import { registerUiSessionFlusher } from "@/services/ui-session";

/** 输入停止后多久自动保存。 */
export const NOTE_SAVE_DEBOUNCE_MS = 800;
/** 正文上限（UTF-8 字节），与后端 maxBodyBytes 一致；超过时不发送保存请求。 */
export const NOTE_BODY_MAX_BYTES = 4 << 20;
/** 标题上限（字符），与后端 maxTitleRunes 一致。 */
export const NOTE_TITLE_MAX_LENGTH = 500;
/** 笔记页上次打开的笔记。笔记是全局资源，不按工作区隔离，因此不走工作区作用域持久化。 */
export const NOTES_LAST_NOTE_STORAGE_KEY = "notes.lastNoteId";

export interface NoteEditPatch {
  title?: string;
  body?: string;
}

function readLastNoteId(): string {
  try {
    return localStorage.getItem(NOTES_LAST_NOTE_STORAGE_KEY) ?? "";
  } catch {
    return "";
  }
}

function writeLastNoteId(id: string) {
  try {
    if (id) localStorage.setItem(NOTES_LAST_NOTE_STORAGE_KEY, id);
    else localStorage.removeItem(NOTES_LAST_NOTE_STORAGE_KEY);
  } catch {
    // 存储不可用时只丢失「上次打开」的记忆，不影响编辑。
  }
}

// 后端只接受单行标题：控制字符（含 Tab、换行）与双向文本控制符换成空格，其余原样保留。
// eslint-disable-next-line no-control-regex
const TITLE_DISALLOWED = /[\u0000-\u001f\u007f-\u009f\u202a-\u202e\u2066-\u2069]/g;

function normalizeTitle(title: string): string {
  const cleaned = title.replace(TITLE_DISALLOWED, " ");
  const chars = [...cleaned];
  return chars.length > NOTE_TITLE_MAX_LENGTH ? chars.slice(0, NOTE_TITLE_MAX_LENGTH).join("") : cleaned;
}

/** 正文的 UTF-8 字节数是否超过上限；字符数 ×3 不超过上限时不必编码。 */
function bodyTooLarge(body: string): boolean {
  if (body.length * 3 <= NOTE_BODY_MAX_BYTES) return false;
  return new TextEncoder().encode(body).length > NOTE_BODY_MAX_BYTES;
}

function omitKey<T>(record: Record<string, T>, key: string): Record<string, T> {
  if (!(key in record)) return record;
  return Object.fromEntries(Object.entries(record).filter(([entry]) => entry !== key));
}

function toSummary(note: Note): NoteSummary {
  const { id, folderId, title, favorite, position, updatedAt, tags } = note;
  return { id, folderId, title, favorite, position, updatedAt, tags: [...tags] };
}

function byPosition<T extends { position: number }>(items: T[]): T[] {
  return [...items].sort((a, b) => a.position - b.position);
}

function sameLink(a: Pick<NoteLink, "objectType" | "objectId" | "scopeId">, b: Pick<NoteLink, "objectType" | "objectId" | "scopeId">) {
  return a.objectType === b.objectType && a.objectId === b.objectId && a.scopeId === b.scopeId;
}

/**
 * 笔记页与关联笔记抽屉共享的笔记状态。每篇已打开笔记只有一个响应式对象，两个编辑器绑定同一份草稿；
 * 编辑按笔记独立防抖保存，保存进行中输入的新内容不会被覆盖或丢失。
 */
export const useNotesStore = defineStore("notes", () => {
  const { t } = useVueI18n();

  const tree = ref<NoteTree | null>(null);
  const loadedNotes = ref<Record<string, Note>>({});
  const saving = ref<Record<string, boolean>>({});
  const saveErrors = ref<Record<string, string>>({});
  const selectedNoteId = ref(readLastNoteId());
  /** 关联增删、笔记或文件夹删除后自增；角标据此失效重取。 */
  const linksVersion = ref(0);

  const loads = new Map<string, Promise<Note>>();
  const timers = new Map<string, ReturnType<typeof setTimeout>>();
  /** 每篇笔记的本地编辑版本号与已保存到的版本号；版本号更大即有未保存内容。 */
  const revisions = new Map<string, number>();
  const savedRevisions = new Map<string, number>();
  const inFlight = new Map<string, Promise<boolean>>();
  let treeRequest = 0;

  function isDirty(id: string) {
    return (revisions.get(id) ?? 0) > (savedRevisions.get(id) ?? 0);
  }

  function treeSummary(id: string) {
    return tree.value?.notes.find((item) => item.id === id);
  }

  function clearTimer(id: string) {
    const timer = timers.get(id);
    if (timer !== undefined) clearTimeout(timer);
    timers.delete(id);
  }

  function scheduleSave(id: string) {
    clearTimer(id);
    timers.set(id, setTimeout(() => {
      timers.delete(id);
      void flushNote(id);
    }, NOTE_SAVE_DEBOUNCE_MS));
  }

  /** 丢弃一篇笔记的全部本地状态（笔记已在后端删除）。 */
  function forgetNote(id: string) {
    clearTimer(id);
    loads.delete(id);
    revisions.delete(id);
    savedRevisions.delete(id);
    inFlight.delete(id);
    loadedNotes.value = omitKey(loadedNotes.value, id);
    saving.value = omitKey(saving.value, id);
    saveErrors.value = omitKey(saveErrors.value, id);
    if (selectedNoteId.value === id) selectNote("");
  }

  function applySummary(id: string, patch: Partial<NoteSummary>) {
    const loaded = loadedNotes.value[id];
    if (loaded) Object.assign(loaded, patch);
    const item = treeSummary(id);
    if (item) Object.assign(item, patch);
  }

  /** 发送当前快照；只在没有更新的编辑时才视为已保存。返回是否成功。 */
  function startSave(id: string): Promise<boolean> {
    const note = loadedNotes.value[id]!;
    const revision = revisions.get(id) ?? 0;
    const payload = { id, title: note.title, body: note.body };
    saving.value = { ...saving.value, [id]: true };
    // 回调总是异步执行，此时 promise 已赋值；笔记被删除后 inFlight 不再指向它，结果直接丢弃。
    const promise: Promise<boolean> = (async () => notesService.updateNote(payload))()
      .then((result) => {
        if (inFlight.get(id) !== promise) return true;
        savedRevisions.set(id, Math.max(savedRevisions.get(id) ?? 0, revision));
        saveErrors.value = omitKey(saveErrors.value, id);
        // 只取更新时间：标题与正文以本地为准，避免覆盖保存期间的新输入。
        applySummary(id, { updatedAt: result.updatedAt });
        if (isDirty(id) && !timers.has(id)) scheduleSave(id);
        return true;
      }, (error: unknown) => {
        if (inFlight.get(id) !== promise) return false;
        saveErrors.value = { ...saveErrors.value, [id]: notesService.noteErrorMessage(error, t) };
        return false;
      })
      .finally(() => {
        if (inFlight.get(id) !== promise) return;
        inFlight.delete(id);
        saving.value = omitKey(saving.value, id);
      });
    inFlight.set(id, promise);
    return promise;
  }

  function selectNote(id: string) {
    selectedNoteId.value = id;
    writeLastNoteId(id);
  }

  async function loadTree() {
    const request = ++treeRequest;
    const next = await notesService.getNoteTree();
    if (request !== treeRequest) return;
    // 有未保存标题的笔记保留本地标题。
    for (const item of next.notes) {
      const loaded = loadedNotes.value[item.id];
      if (loaded && isDirty(item.id)) item.title = loaded.title;
    }
    tree.value = next;
  }

  /** 打开笔记；同一 id 始终返回同一个响应式对象，并发打开复用同一次加载。 */
  function openNote(id: string): Promise<Note> {
    const loaded = loadedNotes.value[id];
    if (loaded) return Promise.resolve(loaded);
    const pending = loads.get(id);
    if (pending) return pending;
    const promise: Promise<Note> = (async () => notesService.getNote(id))()
      .then((note) => {
        const existing = loadedNotes.value[id];
        if (existing) return existing;
        // 加载期间笔记已被删除：不再放回。
        if (loads.get(id) !== promise) throw new Error("note_not_found");
        loadedNotes.value = { ...loadedNotes.value, [id]: note };
        return loadedNotes.value[id]!;
      })
      .finally(() => {
        if (loads.get(id) === promise) loads.delete(id);
      });
    loads.set(id, promise);
    return promise;
  }

  /** 立即更新共享草稿与树中标题，并为该笔记安排防抖保存。未打开的笔记忽略。 */
  function editNote(id: string, patch: NoteEditPatch) {
    const note = loadedNotes.value[id];
    if (!note) return;
    let changed = false;
    const title = patch.title === undefined ? undefined : normalizeTitle(patch.title);
    if (title !== undefined && title !== note.title) {
      note.title = title;
      const item = treeSummary(id);
      if (item) item.title = title;
      changed = true;
    }
    if (patch.body !== undefined && patch.body !== note.body) {
      note.body = patch.body;
      changed = true;
    }
    if (!changed) return;
    revisions.set(id, (revisions.get(id) ?? 0) + 1);
    scheduleSave(id);
  }

  /** 等待进行中的保存，再保存剩余改动。失败不抛出，错误写入 saveErrors。 */
  async function flushNote(id: string): Promise<void> {
    clearTimer(id);
    for (;;) {
      const running = inFlight.get(id);
      if (running) {
        await running;
        continue;
      }
      const note = loadedNotes.value[id];
      if (!note || !isDirty(id)) return;
      clearTimer(id);
      // 标题与正文一起保存：正文超限时后端拒绝整个请求，这里直接提示，正文留在内存中等待用户拆分。
      if (bodyTooLarge(note.body)) {
        saveErrors.value = { ...saveErrors.value, [id]: t("notes.error.note_body_too_large") };
        return;
      }
      if (!(await startSave(id))) return;
    }
  }

  async function flushAll(): Promise<void> {
    const ids = new Set([...timers.keys(), ...inFlight.keys()]);
    for (const id of revisions.keys()) if (isDirty(id)) ids.add(id);
    await Promise.all([...ids].map((id) => flushNote(id)));
  }

  async function createNote(input: CreateNoteInput = {}): Promise<Note> {
    const created = await notesService.createNote(input);
    loadedNotes.value = { ...loadedNotes.value, [created.id]: created };
    if (tree.value) tree.value.notes = byPosition([...tree.value.notes, toSummary(created)]);
    return loadedNotes.value[created.id]!;
  }

  async function deleteNote(id: string) {
    await notesService.deleteNote(id);
    if (tree.value) tree.value.notes = tree.value.notes.filter((item) => item.id !== id);
    forgetNote(id);
    linksVersion.value++;
  }

  async function moveNote(id: string, folderId: string, position: number) {
    await notesService.moveNote(id, folderId, position);
    applySummary(id, { folderId, position });
    if (tree.value) tree.value.notes = byPosition(tree.value.notes);
  }

  async function setFavorite(id: string, favorite: boolean) {
    const result = await notesService.updateNote({ id, favorite });
    applySummary(id, { favorite: result.favorite, updatedAt: result.updatedAt });
  }

  async function setTags(id: string, tags: string[]) {
    const result = await notesService.setNoteTags(id, tags);
    applySummary(id, { tags: result.tags, updatedAt: result.updatedAt });
  }

  /**
   * 添加（或更新快照）一条关联。AI 对话可以移动到别的工作区：同一篇笔记里该对话记录在其他 scope 下的
   * 旧关联随之移除（先加新的再删旧的，删失败只留下一条重复关联，不会丢失关联）。只处理已打开的笔记。
   */
  async function addLink(id: string, input: NoteLinkInput): Promise<NoteLink> {
    const link = await notesService.addNoteLink(id, input);
    const note = loadedNotes.value[id];
    if (note) note.links = [...note.links.filter((item) => !sameLink(item, link)), link];
    linksVersion.value++;
    const stale = link.objectType === "ai"
      ? (note?.links ?? []).filter((item) => item.objectType === "ai" && item.objectId === link.objectId && item.scopeId !== link.scopeId)
      : [];
    for (const item of stale) await removeLink(id, item.objectType, item.objectId, item.scopeId);
    return link;
  }

  async function removeLink(id: string, objectType: NoteLinkType, objectId: string, scopeId = "") {
    await notesService.removeNoteLink(id, objectType, objectId, scopeId);
    const note = loadedNotes.value[id];
    if (note) note.links = note.links.filter((item) => !sameLink(item, { objectType, objectId, scopeId }));
    linksVersion.value++;
  }

  async function createFolder(name: string, parentId = ""): Promise<NoteFolder> {
    const folder = await notesService.createNoteFolder(name, parentId);
    if (tree.value) tree.value.folders = byPosition([...tree.value.folders, folder]);
    return folder;
  }

  async function renameFolder(id: string, name: string) {
    await notesService.renameNoteFolder(id, name);
    const folder = tree.value?.folders.find((item) => item.id === id);
    if (folder) folder.name = name;
  }

  async function moveFolder(id: string, parentId: string, position: number) {
    await notesService.moveNoteFolder(id, parentId, position);
    if (!tree.value) return;
    const folder = tree.value.folders.find((item) => item.id === id);
    if (folder) Object.assign(folder, { parentId, position });
    tree.value.folders = byPosition(tree.value.folders);
  }

  /** 删除文件夹；后端级联删除子文件夹及其中笔记，本地按树同步移除。 */
  async function deleteFolder(id: string) {
    await notesService.deleteNoteFolder(id);
    if (tree.value) {
      const removed = new Set([id]);
      for (let grew = true; grew;) {
        grew = false;
        for (const folder of tree.value.folders) {
          if (!removed.has(folder.id) && removed.has(folder.parentId)) {
            removed.add(folder.id);
            grew = true;
          }
        }
      }
      const removedNotes = tree.value.notes.filter((item) => removed.has(item.folderId));
      tree.value.folders = tree.value.folders.filter((folder) => !removed.has(folder.id));
      tree.value.notes = tree.value.notes.filter((item) => !removed.has(item.folderId));
      for (const item of removedNotes) forgetNote(item.id);
    }
    linksVersion.value++;
  }

  if (typeof window !== "undefined") {
    const onBeforeUnload = () => void flushAll();
    window.addEventListener("beforeunload", onBeforeUnload);
    // 桌面退出（app:before-quit）走 App 的 flushPersistentUiState，那里会等待这些保存完成。
    const stopUiSessionFlusher = registerUiSessionFlusher(flushAll);
    onScopeDispose(() => {
      window.removeEventListener("beforeunload", onBeforeUnload);
      stopUiSessionFlusher();
      for (const timer of timers.values()) clearTimeout(timer);
      timers.clear();
    });
  }

  return {
    tree,
    loadedNotes,
    saving,
    saveErrors,
    selectedNoteId,
    linksVersion,
    selectNote,
    loadTree,
    openNote,
    editNote,
    flushNote,
    flushAll,
    createNote,
    deleteNote,
    moveNote,
    setFavorite,
    setTags,
    addLink,
    removeLink,
    createFolder,
    renameFolder,
    moveFolder,
    deleteFolder,
  };
});

if (import.meta.hot) import.meta.hot.accept(acceptHMRUpdate(useNotesStore, import.meta.hot));
