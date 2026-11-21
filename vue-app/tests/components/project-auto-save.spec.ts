import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { createApp, nextTick, ref } from "vue";
import { useProjectAutoSave } from "@/composables/use-project-auto-save";
import { useLocalProjectStore } from "@/stores/local-project";
import { DEFAULT_EDITOR_PREFERENCES, sanitizeEditorPreferences, upgradeStoredEditorPreferences, type ProjectEditorDocument } from "@/domain/projects/types";
import { cleanSavedText, isEditableProjectText } from "@/domain/projects/save-policy";
import { createProjectSaveCoordinator } from "@/services/project-save-coordinator";

vi.mock("@/services/persistent-store", () => ({
  loadPersistedValue: vi.fn().mockResolvedValue(null), savePersistedValue: vi.fn().mockResolvedValue(undefined),
}));
const dispose: Array<() => void> = [];
afterEach(() => { dispose.splice(0).forEach((fn) => fn()); vi.useRealTimers(); });
beforeEach(() => vi.useFakeTimers());
function doc(path = "/repo/a.txt", sessionId = 1): ProjectEditorDocument {
  return { path, sessionId, name: "a.txt", content: "changed", savedContent: "old", loaded: true, loading: false };
}
function setup() {
  const active = ref(true), scope = ref("one"), documents = ref([doc()]);
  // Timings below are written for a 1 s delay, independent of the default.
  const preferences = ref({ ...DEFAULT_EDITOR_PREFERENCES, autoSave: "afterDelay" as const, autoSaveDelay: 1000 });
  const save = vi.fn().mockResolvedValue(undefined);
  let scheduler!: ReturnType<typeof useProjectAutoSave>;
  const app = createApp({ setup() {
    scheduler = useProjectAutoSave({ active, scope: () => scope.value, documents: () => documents.value,
      preferences: () => preferences.value, enabled: () => true, save });
    return () => null;
  } });
  app.mount(document.createElement("div")); dispose.push(() => app.unmount());
  return { active, scope, documents, preferences, save, scheduler };
}

describe("project auto save", () => {
  it("debounces every edit even when dirty was already true", async () => {
    const s = setup();
    await vi.advanceTimersByTimeAsync(900); s.scheduler.edited("/repo/a.txt");
    await vi.advanceTimersByTimeAsync(900); expect(s.save).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(100); expect(s.save).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(10000); expect(s.save).toHaveBeenCalledTimes(1);
  });
  it("cancels timers on deactivation and resumes eligible dirty documents", async () => {
    const s = setup(); s.active.value = false;
    await vi.advanceTimersByTimeAsync(1500); expect(s.save).not.toHaveBeenCalled();
    s.active.value = true;
    await vi.advanceTimersByTimeAsync(1000); expect(s.save).toHaveBeenCalledTimes(1);
  });
  it("invalidates queued work on setting changes or scope changes", async () => {
    const s = setup();
    await vi.advanceTimersByTimeAsync(1000);
    const valid = s.save.mock.calls[0][1]; expect(valid()).toBe(true);
    s.scope.value = "two"; expect(valid()).toBe(false);
    s.preferences.value = { ...s.preferences.value, autoSave: "off" as never };
    await vi.advanceTimersByTimeAsync(1000); expect(s.save).toHaveBeenCalledTimes(1);
  });
  it("captures model departure even without a widget blur, with no delay save", async () => {
    const s = setup(); s.preferences.value = { ...s.preferences.value, autoSave: "onFocusChange" as never };
    s.scheduler.departure("/repo/a.txt"); await nextTick();
    expect(s.save).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(5000); expect(s.save).toHaveBeenCalledTimes(1);
  });
  it("saves on window blur only for eligible dirty text", async () => {
    const s = setup();
    s.documents.value.push({ ...doc("/repo/photo.png", 2) });
    s.preferences.value = { ...s.preferences.value, autoSave: "onWindowChange" as never };
    window.dispatchEvent(new Event("blur")); await nextTick();
    expect(s.save).toHaveBeenCalledTimes(1);
  });
  it("does not save clean, failed, binary or custom documents", async () => {
    const s = setup();
    s.documents.value = [ { ...doc(), content: "old" }, { ...doc("/b", 2), loading: true },
      { ...doc("/c", 3), externalKind: "binary" }, { ...doc("/d", 4), editor: { pluginId: "p", editorId: "e" } } ];
    await vi.advanceTimersByTimeAsync(5000); expect(s.save).not.toHaveBeenCalled();
    expect(isEditableProjectText({ ...doc(), path: "/a.pdf" })).toBe(false);
  });
  it("invalidates a closed/reopened document at the same path", async () => {
    const s = setup(); await vi.advanceTimersByTimeAsync(1000);
    const valid = s.save.mock.calls[0][1];
    s.documents.value = [doc("/repo/a.txt", 2)];
    expect(valid()).toBe(false);
  });
  it("keeps request ownership valid when formatting returns the model to its saved text", async () => {
    const s = setup(); await vi.advanceTimersByTimeAsync(1000);
    const valid = s.save.mock.calls[0][1];
    s.documents.value[0].content = s.documents.value[0].savedContent;
    expect(valid()).toBe(true);
    s.scheduler.edited("/repo/a.txt"); await vi.advanceTimersByTimeAsync(1000);
    expect(s.save).toHaveBeenCalledTimes(1);
  });
  it("queues the newest edit while an earlier write is still pending", async () => {
    const s = setup(); const queue = createProjectSaveCoordinator();
    let finish!: () => void;
    const writes: string[] = [];
    s.save.mockImplementation((path: string, valid: () => boolean) => queue.save(path, async () => {
      if (!valid()) return "cancelled";
      const content = s.documents.value[0].content; writes.push(content);
      if (writes.length === 1) await new Promise<void>((resolve) => { finish = resolve; });
      s.documents.value[0].savedContent = content;
      return "saved";
    }, "auto"));
    await vi.advanceTimersByTimeAsync(1000);
    s.documents.value[0].content = "newest"; s.scheduler.edited("/repo/a.txt");
    await vi.advanceTimersByTimeAsync(1000);
    expect(writes).toEqual(["changed"]);
    finish(); await vi.advanceTimersByTimeAsync(0);
    expect(writes).toEqual(["changed", "newest"]);
    expect(s.documents.value[0].savedContent).toBe("newest");
  });
  it("coalesces duplicate departure events before the save queue", async () => {
    const s = setup(); s.preferences.value = { ...s.preferences.value, autoSave: "onFocusChange" as never };
    s.scheduler.departure("/repo/a.txt"); s.scheduler.departure("/repo/a.txt"); await nextTick();
    expect(s.save).toHaveBeenCalledTimes(1);
  });
  it("blocks repeated failure triggers until a new edit and cancels formatter-generated timers", async () => {
    const s = setup(); s.preferences.value = { ...s.preferences.value, autoSave: "onFocusChange" as never };
    s.scheduler.departure("/repo/a.txt"); await nextTick();
    s.scheduler.failed("/repo/a.txt");
    s.scheduler.departure("/repo/a.txt"); await nextTick();
    expect(s.save).toHaveBeenCalledTimes(1);
    s.scheduler.edited("/repo/a.txt"); s.scheduler.departure("/repo/a.txt"); await nextTick();
    expect(s.save).toHaveBeenCalledTimes(2);
    s.preferences.value = { ...s.preferences.value, autoSave: "afterDelay" as never };
    s.scheduler.edited("/repo/a.txt"); s.scheduler.failed("/repo/a.txt");
    await vi.advanceTimersByTimeAsync(10000); expect(s.save).toHaveBeenCalledTimes(2);
  });
  it("retains failed dirty edits without creating a retry loop", async () => {
    const s = setup(); s.save.mockRejectedValue(new Error("no write"));
    await vi.advanceTimersByTimeAsync(1000); await vi.advanceTimersByTimeAsync(10000);
    expect(s.save).toHaveBeenCalledTimes(1); expect(s.documents.value[0].content).toBe("changed");
  });
});

describe("project save policy and acknowledgement", () => {
  it("adds safe defaults to old preferences and rejects malformed values", () => {
    const prefs = sanitizeEditorPreferences({ fontSize: 15, formatOnPaste: false });
    expect(prefs).toEqual({ ...DEFAULT_EDITOR_PREFERENCES, fontSize: 15, formatOnPaste: false });
    expect(sanitizeEditorPreferences({ autoSaveDelay: NaN, autoSave: "invalid" as never,
      indentationGuides: "false" as never }).autoSave).toBe("off");
    expect(sanitizeEditorPreferences({ autoSaveDelay: Infinity }).autoSaveDelay).toBe(3000);
    expect(sanitizeEditorPreferences({ autoSaveDelay: 500 }).autoSaveDelay).toBe(500);
  });
  it("moves the stored old 1 s default to 3 s unless after-delay saving already used it", () => {
    const load = (value: Parameters<typeof upgradeStoredEditorPreferences>[0]) =>
      sanitizeEditorPreferences(upgradeStoredEditorPreferences(value)).autoSaveDelay;
    expect(load({ autoSave: "off", autoSaveDelay: 1000 })).toBe(3000);
    expect(load({ autoSave: "onFocusChange", autoSaveDelay: 1000 })).toBe(3000);
    expect(load({ autoSave: "afterDelay", autoSaveDelay: 1000 })).toBe(1000);
    expect(load({ autoSave: "off", autoSaveDelay: 1500 })).toBe(1500);
    expect(load(undefined)).toBe(3000);
  });
  it("preserves line endings, ignores empty files and cleans only when opted in", () => {
    const policy = { trimTrailingWhitespace: true, insertFinalNewline: true };
    expect(cleanSavedText("a \r\nb\t", policy)).toBe("a\r\nb\r\n");
    expect(cleanSavedText("", policy)).toBe("");
    expect(cleanSavedText("a  ", DEFAULT_EDITOR_PREFERENCES)).toBe("a  ");
    expect(cleanSavedText("a \nb \n", policy)).toBe("a\nb\n");
  });
  it("serializes manual and automatic writes for the same physical target", async () => {
    const queue = createProjectSaveCoordinator(), writes: string[] = [];
    let finish!: () => void;
    const first = queue.save("a", async () => { writes.push("old"); await new Promise<void>((r) => { finish = r; }); return "saved"; });
    const second = queue.save("a", async () => { writes.push("new"); return "saved"; });
    await Promise.resolve(); await Promise.resolve();
    expect(writes).toEqual(["old"]); finish();
    await Promise.all([first, second]); expect(writes).toEqual(["old", "new"]);
    await queue.wait("a");
  });
  it("acknowledges the owning cached document after A→B→A, not a reopened tab", async () => {
    const store = useLocalProjectStore(); await store.hydrate("a");
    store.setDocumentLoaded("/repo/a.txt", "old");
    const session = store.activeDocument!.sessionId;
    store.updateDocumentContent("/repo/a.txt", "new");
    store.switchWorkspace("b");
    store.acknowledgeDocumentSaved("a", "/repo/a.txt", session, "old");
    store.switchWorkspace("a");
    expect(store.activeDocument?.content).toBe("new");
    expect(store.dirtyCount).toBe(1);
    store.closeDocument("/repo/a.txt"); store.setDocumentLoaded("/repo/a.txt", "replacement");
    store.acknowledgeDocumentSaved("a", "/repo/a.txt", session, "old");
    expect(store.activeDocument?.savedContent).toBe("replacement");
  });
  it("persists separate scope preferences and resets only the current scope", async () => {
    const store = useLocalProjectStore(); await store.hydrate("a");
    store.updateEditorPreferences({ autoSave: "afterDelay" });
    store.switchWorkspace("b"); expect(store.editorPreferences.autoSave).toBe("off");
    store.updateEditorPreferences({ formatOnSave: true });
    store.switchWorkspace("a"); expect(store.editorPreferences.autoSave).toBe("afterDelay");
    expect(store.editorPreferences.formatOnSave).toBe(false);
  });
});

describe("save queue coalescing", () => {
  it("retains the latest automatic request without dropping manual writes", async () => {
    const queue = createProjectSaveCoordinator(), writes: string[] = [];
    let finish!: () => void;
    const first = queue.save("a", async () => { await new Promise<void>((r) => { finish = r; }); return "saved"; });
    await Promise.resolve(); await Promise.resolve();
    const one = queue.save("a", async () => { writes.push("stale"); return "saved"; }, "scope:session");
    const two = queue.save("a", async () => { writes.push("latest"); return "saved"; }, "scope:session");
    expect(one).toBe(two);
    const manual = queue.save("a", async () => { writes.push("manual"); return "saved"; });
    finish(); await Promise.all([first, two, manual]);
    expect(writes).toEqual(["latest", "manual"]);
  });
});
