import { computed, ref, watch } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import {
  normalizedProjectRoot,
  pendingEditStats,
  pendingEditsForProjectRoot,
  stagePendingEdit,
  type PendingEditStatus,
  type PendingFileEdit,
  type StagePendingEditInput,
} from "@/domain/ai/pending-edits";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";
import { invokeBackend } from "@/services/desktop-bridge";

const AI_PENDING_EDITS_KEY = "app_ai_pending_edits_v1";
const REVERT_CONFLICT_ERROR = "ai_edit_revert_conflict";
const PERSIST_DELAY_MS = 120;
const PERSISTED_STATUSES = new Set<PendingEditStatus>(["pending", "accepted", "failed"]);

interface PersistedPendingEdits {
  version: 1;
  selectedId: string;
  edits: PendingFileEdit[];
}

function createId() {
  return globalThis.crypto?.randomUUID?.() ?? `edit-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

function isMissingPathError(error: unknown) {
  const message = errorMessage(error).toLowerCase();
  return message.includes("no such file") || message.includes("not exist") || message.includes("cannot find the file");
}

function restorePendingEdit(value: unknown): PendingFileEdit | undefined {
  if (!value || typeof value !== "object") return undefined;
  const candidate = value as Partial<PendingFileEdit>;
  if (
    typeof candidate.id !== "string" || !candidate.id
    || typeof candidate.path !== "string" || !candidate.path
    || typeof candidate.projectRoot !== "string" || !candidate.projectRoot
    || (candidate.before !== null && typeof candidate.before !== "string")
    || typeof candidate.after !== "string"
    || typeof candidate.status !== "string" || !PERSISTED_STATUSES.has(candidate.status as PendingEditStatus)
    || typeof candidate.createdAt !== "number" || !Number.isFinite(candidate.createdAt)
  ) return undefined;

  return {
    id: candidate.id,
    path: candidate.path,
    projectRoot: candidate.projectRoot,
    before: candidate.before,
    after: candidate.after,
    // An accepted edit was mid-revert when the app stopped. Restore it as
    // pending; revert() re-checks the current file before writing anything.
    status: candidate.status === "accepted" ? "pending" : candidate.status,
    error: typeof candidate.error === "string" ? candidate.error : undefined,
    createdAt: candidate.createdAt,
  };
}

function restorePendingEdits(value: unknown): PersistedPendingEdits | undefined {
  if (!value || typeof value !== "object") return undefined;
  const candidate = value as Partial<PersistedPendingEdits>;
  if (candidate.version !== 1 || !Array.isArray(candidate.edits)) return undefined;
  return {
    version: 1,
    selectedId: typeof candidate.selectedId === "string" ? candidate.selectedId : "",
    edits: candidate.edits.map(restorePendingEdit).filter((edit): edit is PendingFileEdit => Boolean(edit)),
  };
}

export const useAiPendingEditsStore = defineStore("ai-pending-edits", () => {
  const edits = ref<PendingFileEdit[]>([]);
  const applying = ref(false);
  const reviewOpen = ref(false);
  const selectedId = ref("");
  const hydrated = ref(false);

  const actionable = computed(() => edits.value.filter((edit) => ["pending", "accepted", "failed"].includes(edit.status)));
  const stats = computed(() => pendingEditStats(actionable.value));
  const selected = computed(() => actionable.value.find((edit) => edit.id === selectedId.value) ?? actionable.value[0]);

  function persistedSnapshot(): PersistedPendingEdits {
    const persistedEdits = actionable.value.map((edit) => ({ ...edit }));
    return {
      version: 1,
      selectedId: persistedEdits.some((edit) => edit.id === selectedId.value) ? selectedId.value : "",
      edits: persistedEdits,
    };
  }

  let persistTimer: ReturnType<typeof setTimeout> | undefined;
  async function flushPersistence() {
    if (!hydrated.value) return;
    if (persistTimer) {
      clearTimeout(persistTimer);
      persistTimer = undefined;
    }
    await savePersistedValue(AI_PENDING_EDITS_KEY, persistedSnapshot());
  }

  async function hydrate() {
    if (hydrated.value) return;
    const restored = restorePendingEdits(await loadPersistedValue<unknown>(AI_PENDING_EDITS_KEY));
    if (restored) {
      const fileKey = (edit: PendingFileEdit) => `${normalizedProjectRoot(edit.projectRoot)}\0${edit.path}`;
      const stagedNow = new Set(edits.value.map(fileKey));
      edits.value = [
        ...restored.edits.filter((edit) => !stagedNow.has(fileKey(edit))),
        ...edits.value,
      ];
      selectedId.value = edits.value.some((edit) => edit.id === selectedId.value)
        ? selectedId.value
        : restored.selectedId;
    }
    selectedId.value = actionable.value.some((edit) => edit.id === selectedId.value)
      ? selectedId.value
      : actionable.value[0]?.id ?? "";
    hydrated.value = true;
    await flushPersistence();
  }

  function stage(input: StagePendingEditInput) {
    const result = stagePendingEdit(
      // 已保留/已撤销的记录不再可操作，顺手清理，避免会话越久列表越大。
      edits.value.filter((edit) => edit.status !== "applied" && edit.status !== "rejected"),
      input,
      createId,
    );
    edits.value = result.edits;
    if (!actionable.value.some((edit) => edit.id === selectedId.value)) selectedId.value = result.edit.id;
    return result.edit;
  }
  function setStatus(id: string, status: PendingFileEdit["status"], error?: string) {
    edits.value = edits.value.map((edit) => edit.id === id ? { ...edit, status, error } : edit);
  }
  function scopedActionable(projectRoot: string) {
    return pendingEditsForProjectRoot(actionable.value, projectRoot);
  }
  function finishReview(projectRoot: string) {
    const remaining = scopedActionable(projectRoot);
    selectedId.value = remaining[0]?.id ?? "";
    reviewOpen.value = remaining.length > 0 && reviewOpen.value;
  }
  function apply(projectRoot: string, ids = scopedActionable(projectRoot).map((edit) => edit.id)) {
    if (applying.value) return { applied: [] as string[], failed: [] as Array<{ path: string; error: string }> };
    const applied: string[] = [];
    for (const edit of scopedActionable(projectRoot).filter((item) => ids.includes(item.id))) {
      setStatus(edit.id, "applied");
      applied.push(edit.path);
    }
    finishReview(projectRoot);
    return { applied, failed: [] as Array<{ path: string; error: string }> };
  }
  async function revert(
    projectRoot: string,
    ids = scopedActionable(projectRoot).map((edit) => edit.id),
  ) {
    if (applying.value) return { reverted: [] as string[], failed: [] as Array<{ path: string; error: string }> };
    applying.value = true;
    const reverted: string[] = [];
    const failed: Array<{ path: string; error: string }> = [];
    try {
      for (const edit of scopedActionable(projectRoot).filter((item) => ids.includes(item.id))) {
        setStatus(edit.id, "accepted");
        try {
          let current: string | null = null;
          try {
            current = await invokeBackend<string>("local_fs_read_text_file", {
              projectRoot: edit.projectRoot,
              filePath: edit.path,
            });
          } catch (error) {
            if (!isMissingPathError(error)) throw error;
          }
          if (current === null) {
            if (edit.before !== null) throw new Error(REVERT_CONFLICT_ERROR);
          } else if (current !== edit.after) {
            throw new Error(REVERT_CONFLICT_ERROR);
          }
          if (edit.before === null) {
            if (current !== null) {
              await invokeBackend("local_fs_delete", {
                projectRoot: edit.projectRoot,
                path: edit.path,
              });
            }
          } else {
            await invokeBackend("local_fs_write_text_file", {
              projectRoot: edit.projectRoot,
              filePath: edit.path,
              content: edit.before,
            });
          }
          setStatus(edit.id, "rejected");
          reverted.push(edit.path);
        } catch (error) {
          const message = errorMessage(error);
          setStatus(edit.id, "failed", message);
          failed.push({ path: edit.path, error: message });
        }
      }
      finishReview(projectRoot);
      return { reverted, failed };
    } finally {
      applying.value = false;
    }
  }
  function openReview(projectRoot: string, id?: string) {
    const scoped = scopedActionable(projectRoot);
    const target = scoped.find((edit) => edit.id === id) ?? scoped[0];
    if (!target) return;
    selectedId.value = target.id;
    reviewOpen.value = true;
  }

  watch(
    [edits, selectedId],
    () => {
      if (!hydrated.value) return;
      if (persistTimer) clearTimeout(persistTimer);
      persistTimer = setTimeout(() => {
        persistTimer = undefined;
        void flushPersistence();
      }, PERSIST_DELAY_MS);
    },
    { deep: true },
  );

  return {
    edits, actionable, stats, selected, applying, reviewOpen, selectedId, hydrated,
    hydrate, flushPersistence, stage, apply, revert, openReview,
  };
});

// Pinia store 实例会被缓存：模块热更新后 useStore 仍返回旧实例，
// 闭包里握着的还是旧函数，于是「代码已更新但行为没变」。
// 参见 https://pinia.vuejs.org/cookbook/hot-module-replacement.html
if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useAiPendingEditsStore, import.meta.hot));
}
