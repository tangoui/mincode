import { computed, onBeforeUnmount, ref, watch } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";
import { isWorkspaceScopePermanentlyDeleted } from "@/services/workspace-deleted-scopes";
import { isStaleDebugSourceTabPath } from "@/domain/debug/virtual-source";
import {
  migrateLegacyProjectWorkspace,
  normalizeProjectWorkspaceId,
  PROJECT_WORKSPACE_COLLECTION_VERSION,
  restoreProjectWorkspaceCollection,
  type PersistedProjectWorkspaceCollection,
} from "@/domain/projects/workspace-persistence";
import {
  DEFAULT_EDITOR_PREFERENCES,
  projectBasename,
  sanitizeEditorPreferences,
  upgradeStoredEditorPreferences,
  type ProjectDocumentEditor,
  type ProjectEditorDocument,
  type ProjectDocumentErrorKey,
  type ProjectEditorPreferences,
  type ProjectEditorRevealPosition,
  type ProjectEditorViewState,
  type ProjectTerminalTab,
} from "@/domain/projects/types";
import {
  DEFAULT_LSP_SETTINGS,
  sanitizeLspSettings,
  type LspProjectSettings,
} from "@/domain/projects/lsp";
import { isProjectDocumentDirty, setProjectDocumentDirty } from "@/domain/projects/document-dirty";
import { isBrowserProjectPath, type BrowserProjectAccess } from "@/domain/projects/browser-root";
import { customEditorKey, parseCustomEditorKey } from "@/domain/plugins/custom-editors";

const STORAGE_KEY = "app_local_project_workspaces_v1";
const LEGACY_STORAGE_KEY = "app_local_project_v1";
const FALLBACK_WORKSPACE_ID = "workspace:default";
const MAX_RECENT_PROJECTS = 12;
const MAX_PROJECT_TERMINALS = 50;
const VIEW_STATE_PERSIST_DELAY_MS = 2_000;

export interface LocalProjectEntry {
  root: string;
  name: string;
}

interface PersistedProjectWorkspace {
  version: 6;
  projectRoot: string;
  projects: LocalProjectEntry[];
  openPaths: string[];
  activePath: string | null;
  editorPreferences: ProjectEditorPreferences;
  editorViewStates: Record<string, ProjectEditorViewState>;
  projectTerminalTabs: ProjectTerminalTab[];
  activeProjectTerminalId: string | null;
  lspSettings: LspProjectSettings;
  /** contributes.customEditors: open path → `<pluginId>.<editorId>` (optional, version 6 compatible). */
  documentEditors?: Record<string, string>;
}

function normalizeRoot(root: string) {
  let trimmed = root.trim();
  if (trimmed.startsWith("file://")) {
    try {
      trimmed = decodeURIComponent(new URL(trimmed).pathname);
      if (/^\/[A-Za-z]:\//.test(trimmed)) trimmed = trimmed.slice(1);
    } catch {
      // Keep the original value so invalid persisted data can still be surfaced to the user.
    }
  }
  if (trimmed === "/" || /^[A-Za-z]:[\\/]*$/.test(trimmed)) return trimmed;
  return trimmed.replace(/[\\/]+$/, "");
}

function projectName(root: string) {
  return projectBasename(root) || root;
}

function isAbsoluteProjectPath(path: string) {
  return path.startsWith("/") || /^[A-Za-z]:[\\/]/.test(path) || isBrowserProjectPath(path);
}

function toDocumentPath(projectRoot: string, path: string) {
  const normalized = path.replace(/\\/g, "/").replace(/^\/+/, "");
  if (isAbsoluteProjectPath(path)) return path.replace(/\\/g, "/");
  return projectRoot ? `${projectRoot.replace(/\/+$/, "")}/${normalized}` : normalized;
}

function sanitizeProjects(value: unknown, currentRoot: string): LocalProjectEntry[] {
  const entries = Array.isArray(value) ? value : [];
  const roots = new Set<string>();
  const result: LocalProjectEntry[] = [];
  for (const entry of entries) {
    if (!entry || typeof entry !== "object") continue;
    const candidate = entry as Partial<LocalProjectEntry>;
    const root = typeof candidate.root === "string" ? normalizeRoot(candidate.root) : "";
    if (!root || roots.has(root)) continue;
    roots.add(root);
    result.push({
      root,
      name:
        typeof candidate.name === "string" && candidate.name.trim()
          ? candidate.name.trim()
          : projectName(root),
    });
    if (result.length >= MAX_RECENT_PROJECTS) break;
  }
  if (currentRoot && !roots.has(currentRoot)) {
    result.unshift({ root: currentRoot, name: projectName(currentRoot) });
  }
  return result.slice(0, MAX_RECENT_PROJECTS);
}

function boundedNumber(value: unknown, fallback = 0) {
  return typeof value === "number" && Number.isFinite(value) && value >= 0
    ? Math.min(value, 10_000_000)
    : fallback;
}

function positiveInteger(value: unknown, fallback = 1) {
  return Number.isInteger(value) && Number(value) > 0
    ? Math.min(Number(value), 10_000_000)
    : fallback;
}

function sanitizeEditorViewState(value: unknown): ProjectEditorViewState | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const state = value as Record<string, unknown>;
  const lineNumber = positiveInteger(state.lineNumber);
  const column = positiveInteger(state.column);
  return {
    lineNumber,
    column,
    selectionStartLineNumber: positiveInteger(state.selectionStartLineNumber, lineNumber),
    selectionStartColumn: positiveInteger(state.selectionStartColumn, column),
    positionLineNumber: positiveInteger(state.positionLineNumber, lineNumber),
    positionColumn: positiveInteger(state.positionColumn, column),
    scrollTop: boundedNumber(state.scrollTop),
    scrollLeft: boundedNumber(state.scrollLeft),
  };
}

function sanitizeEditorViewStates(
  value: unknown,
  openPaths: readonly string[]
): Record<string, ProjectEditorViewState> {
  if (!value || typeof value !== "object" || Array.isArray(value)) return {};
  const allowedPaths = new Set(openPaths);
  const restored: Record<string, ProjectEditorViewState> = {};
  for (const [path, candidate] of Object.entries(value)) {
    if (!allowedPaths.has(path)) continue;
    const state = sanitizeEditorViewState(candidate);
    if (state) restored[path] = state;
  }
  return restored;
}

function sanitizeProjectTerminalTabs(value: unknown): ProjectTerminalTab[] {
  if (!Array.isArray(value)) return [];
  const ids = new Set<string>();
  const restored: ProjectTerminalTab[] = [];
  for (const item of value) {
    if (!item || typeof item !== "object" || Array.isArray(item)) continue;
    const candidate = item as Record<string, unknown>;
    const id = typeof candidate.id === "string" ? candidate.id.trim() : "";
    const name = typeof candidate.name === "string" ? candidate.name.trim() : "";
    const cwd = typeof candidate.cwd === "string" ? candidate.cwd.trim() : "";
    if (!id || id.length > 512 || ids.has(id) || !name || !cwd || cwd.length > 32_768) continue;
    ids.add(id);
    restored.push({ id, name: name.slice(0, 256), cwd });
    if (restored.length >= MAX_PROJECT_TERMINALS) break;
  }
  return restored;
}

function sanitizeDocumentEditors(value: unknown, openPaths: readonly string[]): Record<string, string> {
  if (!value || typeof value !== "object" || Array.isArray(value)) return {};
  const allowed = new Set(openPaths);
  const result: Record<string, string> = {};
  for (const [path, key] of Object.entries(value as Record<string, unknown>)) {
    if (allowed.has(path) && typeof key === "string" && parseCustomEditorKey(key)) result[path] = key;
  }
  return result;
}

function documentEditorKey(document: ProjectEditorDocument) {
  return document.editor ? customEditorKey(document.editor.pluginId, document.editor.editorId) : undefined;
}

function restorePersistedWorkspace(value: unknown): PersistedProjectWorkspace | null {
  try {
    if (!value || typeof value !== "object") return null;
    const stored = value as Record<string, unknown>;
    if (
      (stored.version !== 1 &&
        stored.version !== 2 &&
        stored.version !== 3 &&
        stored.version !== 4 &&
        stored.version !== 5 &&
        stored.version !== 6) ||
      typeof stored.projectRoot !== "string"
    ) {
      return null;
    }
    const projectRoot = normalizeRoot(stored.projectRoot);
    // Debugger source tabs cannot be reopened after their session; older
    // versions also saved java-debug's jdt:// addresses as project files.
    const storedOpenPaths = Array.isArray(stored.openPaths)
      ? stored.openPaths.filter((path): path is string => typeof path === "string" && Boolean(path) && !isStaleDebugSourceTabPath(path))
      : [];
    const openPaths = [...new Set(storedOpenPaths.map((path) => toDocumentPath(projectRoot, path)))];
    const activePath =
      typeof stored.activePath === "string" && !isStaleDebugSourceTabPath(stored.activePath)
        ? toDocumentPath(projectRoot, stored.activePath)
        : null;
    const projectTerminalTabs = sanitizeProjectTerminalTabs(stored.projectTerminalTabs);
    const activeProjectTerminalId =
      typeof stored.activeProjectTerminalId === "string" &&
      projectTerminalTabs.some((tab) => tab.id === stored.activeProjectTerminalId)
        ? stored.activeProjectTerminalId
        : null;
    return {
      version: 6,
      projectRoot,
      projects: sanitizeProjects(stored.projects, projectRoot),
      openPaths,
      activePath: activePath && openPaths.includes(activePath) ? activePath : (openPaths[0] ?? null),
      editorPreferences: sanitizeEditorPreferences(
        upgradeStoredEditorPreferences(stored.editorPreferences as Partial<ProjectEditorPreferences> | undefined)
      ),
      editorViewStates: sanitizeEditorViewStates(stored.editorViewStates, openPaths),
      projectTerminalTabs,
      activeProjectTerminalId,
      lspSettings: sanitizeLspSettings(stored.lspSettings),
      documentEditors: sanitizeDocumentEditors(
        stored.documentEditors && typeof stored.documentEditors === "object" && !Array.isArray(stored.documentEditors)
          ? Object.fromEntries(
              Object.entries(stored.documentEditors as Record<string, unknown>)
                .map(([path, key]) => [toDocumentPath(projectRoot, path), key]),
            )
          : undefined,
        openPaths,
      ),
    };
  } catch {
    return null;
  }
}

function emptyPersistedWorkspace(): PersistedProjectWorkspace {
  return {
    version: 6,
    projectRoot: "",
    projects: [],
    openPaths: [],
    activePath: null,
    editorPreferences: { ...DEFAULT_EDITOR_PREFERENCES },
    editorViewStates: {},
    projectTerminalTabs: [],
    activeProjectTerminalId: null,
    lspSettings: { ...DEFAULT_LSP_SETTINGS },
  };
}

function cloneDocuments(documents: readonly ProjectEditorDocument[]): ProjectEditorDocument[] {
  return documents.map((document) => ({ ...document, loading: false }));
}

let nextDocumentSession = 0;

function placeholderDocument(path: string, editorKey?: string): ProjectEditorDocument {
  const editor = editorKey ? parseCustomEditorKey(editorKey) : undefined;
  return {
    path,
    name: projectBasename(path),
    sessionId: ++nextDocumentSession,
    content: "",
    savedContent: "",
    // A plugin editor reads the file itself through the host; no text load.
    loaded: Boolean(editor),
    loading: false,
    ...(editor ? { editor } : {}),
  };
}

export const useLocalProjectStore = defineStore("local-project", () => {
  const initial = emptyPersistedWorkspace();
  const projectRoot = ref(initial.projectRoot);
  const projects = ref<LocalProjectEntry[]>(initial.projects);
  const documents = ref<ProjectEditorDocument[]>([]);
  const activePath = ref<string | null>(initial.activePath);
  const editorPreferences = ref<ProjectEditorPreferences>(initial.editorPreferences);
  const editorViewStates = ref<Record<string, ProjectEditorViewState>>(initial.editorViewStates);
  const projectTerminalTabs = ref<ProjectTerminalTab[]>(initial.projectTerminalTabs);
  const activeProjectTerminalId = ref<string | null>(initial.activeProjectTerminalId);
  const lspSettings = ref<LspProjectSettings>(initial.lspSettings);
  const activeWorkspaceId = ref(FALLBACK_WORKSPACE_ID);
  const hydrated = ref(false);
  let persistedWorkspaces: Record<string, PersistedProjectWorkspace> = {};
  // persistedWorkspaces is a plain object (snapshots, kept out of reactivity);
  // bumped whenever it changes so allListedRoots() and its watchers follow.
  const stashedRevision = ref(0);
  const runtimeDocuments = new Map<string, ProjectEditorDocument[]>();
  // Live editors hold unsynced edits in their models; flushed before documents are cloned or re-keyed.
  const documentContentFlushers = new Set<() => void>();

  function registerDocumentContentFlusher(flush: () => void) {
    documentContentFlushers.add(flush);
    return () => {
      documentContentFlushers.delete(flush);
    };
  }

  function flushDocumentContent() {
    for (const flush of documentContentFlushers) flush();
  }

  const activeDocument = computed(
    () => documents.value.find((document) => document.path === activePath.value) ?? null
  );
  const openPaths = computed(() => documents.value.map((document) => document.path));
  const documentEditorKeys = computed(() => documents.value.map((document) => documentEditorKey(document) ?? ""));
  const dirtyCount = computed(() => documents.value.filter(isProjectDocumentDirty).length);
  const currentProject = computed(
    () => projects.value.find((entry) => entry.root === projectRoot.value) ?? null
  );

  function snapshot(): PersistedProjectWorkspace {
    const persistedDocuments = documents.value.filter((document) => !document.external);
    return {
      version: 6,
      projectRoot: projectRoot.value,
      projects: projects.value.map((entry) => ({ ...entry })),
      openPaths: persistedDocuments.map((document) => document.path),
      activePath: persistedDocuments.some((document) => document.path === activePath.value)
        ? activePath.value
        : null,
      editorPreferences: sanitizeEditorPreferences(editorPreferences.value),
      editorViewStates: sanitizeEditorViewStates(
        editorViewStates.value,
        persistedDocuments.map((document) => document.path)
      ),
      projectTerminalTabs: sanitizeProjectTerminalTabs(projectTerminalTabs.value),
      activeProjectTerminalId: projectTerminalTabs.value.some(
        (tab) => tab.id === activeProjectTerminalId.value
      )
        ? activeProjectTerminalId.value
        : null,
      lspSettings: sanitizeLspSettings(lspSettings.value),
      documentEditors: Object.fromEntries(persistedDocuments.flatMap((document) => {
        const key = documentEditorKey(document);
        return key ? [[document.path, key]] : [];
      })),
    };
  }

  function applyPersistedWorkspace(
    saved: PersistedProjectWorkspace,
    restoredDocuments?: readonly ProjectEditorDocument[]
  ) {
    projectRoot.value = saved.projectRoot;
    projects.value = saved.projects.map((entry) => ({ ...entry }));
    documents.value = restoredDocuments
      ? cloneDocuments(restoredDocuments)
      : saved.openPaths.map((path) => placeholderDocument(path, saved.documentEditors?.[path]));
    activePath.value = saved.activePath;
    editorPreferences.value = { ...saved.editorPreferences };
    editorViewStates.value = Object.fromEntries(
      Object.entries(saved.editorViewStates).map(([path, state]) => [path, { ...state }])
    );
    projectTerminalTabs.value = saved.projectTerminalTabs.map((tab) => ({ ...tab }));
    activeProjectTerminalId.value = saved.activeProjectTerminalId;
    lspSettings.value = { ...saved.lspSettings };
  }

  function stashActiveWorkspace() {
    const workspaceId = normalizeProjectWorkspaceId(activeWorkspaceId.value);
    if (!workspaceId) return;
    persistedWorkspaces[workspaceId] = snapshot();
    stashedRevision.value += 1;
    runtimeDocuments.set(workspaceId, cloneDocuments(documents.value));
  }

  function projectsForWorkspace(workspaceId: string): LocalProjectEntry[] {
    const normalized = normalizeProjectWorkspaceId(workspaceId);
    if (!normalized) return [];
    const entries = normalized === activeWorkspaceId.value
      ? projects.value
      : persistedWorkspaces[normalized]?.projects ?? [];
    return entries.map(({ root, name }) => ({ root, name }));
  }

  /**
   * The project roots each stashed (not active, not permanently deleted) workspace
   * lists, keyed by the workspace (state scope) id. Reactive.
   */
  function rootsByOtherWorkspace(): Map<string, string[]> {
    void stashedRevision.value; // persistedWorkspaces is not reactive; see stashedRevision.
    return new Map(
      Object.entries(persistedWorkspaces)
        .filter(([workspaceId]) => workspaceId !== activeWorkspaceId.value && !isWorkspaceScopePermanentlyDeleted(workspaceId))
        .map(([workspaceId, saved]) => [
          workspaceId,
          [saved.projectRoot, ...saved.projects.map((entry) => entry.root)].filter(Boolean),
        ]),
    );
  }

  /** The project roots each stashed (not active, not permanently deleted) workspace lists. */
  function stashedWorkspaceRoots(): string[][] {
    return [...rootsByOtherWorkspace().values()];
  }

  /**
   * Every project root listed in any workspace (the active one and the stashed
   * ones, permanently deleted scopes excluded), for features that must notice
   * when a project is no longer open anywhere, such as debug sessions. Reactive.
   */
  function allListedRoots(): string[] {
    const roots = new Set<string>([projectRoot.value, ...projects.value.map((entry) => entry.root)].filter(Boolean));
    for (const listed of stashedWorkspaceRoots()) listed.forEach((root) => roots.add(root));
    return [...roots];
  }

  /** The roots other (stashed) workspaces list, for "is it open elsewhere?" checks. Reactive. */
  function rootsListedByOtherWorkspaces(): string[] {
    return [...new Set(stashedWorkspaceRoots().flat())];
  }

  function projectRootForWorkspace(workspaceId: string) {
    const normalized = normalizeProjectWorkspaceId(workspaceId);
    if (!normalized) return "";
    if (normalized === activeWorkspaceId.value) return projectRoot.value;
    return persistedWorkspaces[normalized]?.projectRoot ?? "";
  }

  function applyWorkspace(workspaceId: string) {
    const saved = persistedWorkspaces[workspaceId] ?? emptyPersistedWorkspace();
    applyPersistedWorkspace(saved, runtimeDocuments.get(workspaceId));
  }

  function persistedCollection(): PersistedProjectWorkspaceCollection<PersistedProjectWorkspace> {
    return {
      version: PROJECT_WORKSPACE_COLLECTION_VERSION,
      workspaces: Object.fromEntries(
        Object.entries(persistedWorkspaces)
          .filter(([workspaceId]) => !isWorkspaceScopePermanentlyDeleted(workspaceId))
          .map(([workspaceId, saved]) => [
            workspaceId,
            restorePersistedWorkspace(saved) ?? emptyPersistedWorkspace(),
          ])
      ),
    };
  }

  function rememberProject(root: string) {
    const normalized = normalizeRoot(root);
    if (!normalized) return;
    // Activating an existing root must not reorder the explorer or trigger a tree rebuild.
    if (projects.value.some((entry) => entry.root === normalized)) return;
    projects.value = [
      { root: normalized, name: projectName(normalized) },
      ...projects.value,
    ].slice(0, MAX_RECENT_PROJECTS);
  }

  let hydrationPromise: Promise<void> | undefined;

  async function hydrate(workspaceId = activeWorkspaceId.value) {
    const targetWorkspaceId =
      normalizeProjectWorkspaceId(workspaceId) ??
      normalizeProjectWorkspaceId(activeWorkspaceId.value) ??
      FALLBACK_WORKSPACE_ID;

    if (hydrated.value) {
      switchWorkspace(targetWorkspaceId);
      return;
    }
    activeWorkspaceId.value = targetWorkspaceId;
    if (hydrationPromise) {
      await hydrationPromise;
      return;
    }

    hydrationPromise = (async () => {
      const [savedCollectionValue, legacyValue] = await Promise.all([
        loadPersistedValue<unknown>(STORAGE_KEY),
        loadPersistedValue<unknown>(LEGACY_STORAGE_KEY),
      ]);
      const savedCollection = restoreProjectWorkspaceCollection(
        savedCollectionValue,
        restorePersistedWorkspace
      );
      const legacyWorkspace = restorePersistedWorkspace(legacyValue);
      const restored = migrateLegacyProjectWorkspace(
        savedCollection,
        activeWorkspaceId.value,
        legacyWorkspace
      );
      persistedWorkspaces = restored.workspaces;
      stashedRevision.value += 1;
      applyWorkspace(activeWorkspaceId.value);
      hydrated.value = true;
      // Rewrite only when a legacy migration or normalization changed the stored collection.
      stashActiveWorkspace();
      if (JSON.stringify(persistedCollection()) !== JSON.stringify(savedCollectionValue)) void flushPersistence();
    })();

    try {
      await hydrationPromise;
    } finally {
      hydrationPromise = undefined;
    }
  }

  function switchWorkspace(workspaceId: string) {
    const targetWorkspaceId = normalizeProjectWorkspaceId(workspaceId);
    if (!targetWorkspaceId || targetWorkspaceId === activeWorkspaceId.value) return;
    if (!hydrated.value) {
      activeWorkspaceId.value = targetWorkspaceId;
      return;
    }

    flushDocumentContent();
    stashActiveWorkspace();
    activeWorkspaceId.value = targetWorkspaceId;
    applyWorkspace(targetWorkspaceId);
    schedulePersist();
  }

  let persistTimer: ReturnType<typeof setTimeout> | undefined;

  function discardWorkspaceState(workspaceId: string) {
    const normalized = normalizeProjectWorkspaceId(workspaceId);
    if (!normalized || normalized === activeWorkspaceId.value) return false;
    delete persistedWorkspaces[normalized];
    runtimeDocuments.delete(normalized);
    stashedRevision.value += 1;
    return true;
  }

  async function flushPersistence() {
    if (persistTimer) clearTimeout(persistTimer);
    persistTimer = undefined;
    if (!hydrated.value) return;
    stashActiveWorkspace();
    await savePersistedValue(STORAGE_KEY, persistedCollection());
  }

  function schedulePersist() {
    if (!hydrated.value) return;
    if (persistTimer) clearTimeout(persistTimer);
    persistTimer = setTimeout(() => void flushPersistence(), 300);
  }

  // Cursor and scroll positions change with every caret move and scroll. They
  // ride along with any other save, and otherwise are written at most every
  // VIEW_STATE_PERSIST_DELAY_MS; a pending save is never postponed by them.
  function scheduleViewStatePersist() {
    if (!hydrated.value || persistTimer) return;
    persistTimer = setTimeout(() => void flushPersistence(), VIEW_STATE_PERSIST_DELAY_MS);
  }

  function resetProject(root: string) {
    const normalized = normalizeRoot(root);
    projectRoot.value = normalized;
    rememberProject(normalized);
    documents.value = [];
    activePath.value = null;
    editorViewStates.value = {};
    projectTerminalTabs.value = [];
    activeProjectTerminalId.value = null;
  }

  function restoreProject(root: string) {
    const normalized = normalizeRoot(root);
    projectRoot.value = normalized;
    rememberProject(normalized);
  }

  function renameProject(root: string, name: string) {
    const normalized = normalizeRoot(root);
    const nextName = name.trim();
    if (!normalized || !nextName) return;
    projects.value = projects.value.map((entry) =>
      entry.root === normalized ? { ...entry, name: nextName } : entry
    );
  }

  function removeProject(root: string) {
    const normalized = normalizeRoot(root);
    projects.value = projects.value.filter((entry) => entry.root !== normalized);
    if (projectRoot.value === normalized) {
      projectRoot.value = "";
      documents.value = [];
      activePath.value = null;
      editorViewStates.value = {};
      projectTerminalTabs.value = [];
      activeProjectTerminalId.value = null;
    }
  }

  function mergeProjects(entries: LocalProjectEntry[]) {
    const existingRoots = new Set(projects.value.map((entry) => entry.root));
    const next = sanitizeProjects([...projects.value, ...entries], projectRoot.value);
    const added = next.filter((entry) => !existingRoots.has(entry.root)).length;
    if (added) projects.value = next;
    return added;
  }

  function openDocument(path: string) {
    let document = documents.value.find((item) => item.path === path);
    if (!document) {
      document = placeholderDocument(path);
      documents.value.push(document);
    }
    activePath.value = path;
    return document;
  }

  function setDocumentLoading(path: string, loading: boolean) {
    const document = documents.value.find((item) => item.path === path) ?? openDocument(path);
    document.loading = loading;
    if (loading) {
      document.errorKey = undefined;
      document.errorDetail = undefined;
    }
  }

  function setDocumentLoaded(
    path: string,
    content: string,
    metadata?: Pick<ProjectEditorDocument, "external" | "externalKind" | "mimeType" | "size" | "textPreviewAvailable">,
  ) {
    const document = documents.value.find((item) => item.path === path) ?? openDocument(path);
    document.sessionId = ++nextDocumentSession;
    document.content = content;
    document.savedContent = content;
    setProjectDocumentDirty(document, false);
    document.loaded = true;
    document.loading = false;
    document.errorKey = undefined;
    document.errorDetail = undefined;
    if (metadata) Object.assign(document, metadata);
  }

  function setDocumentError(path: string, errorKey: ProjectDocumentErrorKey, detail?: string) {
    const document = documents.value.find((item) => item.path === path) ?? openDocument(path);
    document.loading = false;
    document.errorKey = errorKey;
    document.errorDetail = detail;
  }

  /**
   * Switches a project document tab between a plugin custom editor and the
   * built-in text editor. Either way the tab starts clean; the text editor
   * reloads the file from disk.
   */
  function setDocumentCustomEditor(path: string, editor: ProjectDocumentEditor | null) {
    const document = openDocument(path);
    document.sessionId = ++nextDocumentSession;
    document.content = "";
    document.savedContent = "";
    setProjectDocumentDirty(document, false);
    document.errorKey = undefined;
    document.loading = false;
    if (editor) {
      document.editor = { pluginId: editor.pluginId, editorId: editor.editorId };
      document.loaded = true;
    } else {
      delete document.editor;
      document.loaded = false;
    }
    delete editorViewStates.value[path];
  }

  function updateDocumentContent(path: string, content: string) {
    const document = documents.value.find((item) => item.path === path);
    if (!document) return;
    document.content = content;
    setProjectDocumentDirty(document, content !== document.savedContent);
  }

  function setDocumentDirty(path: string, dirty: boolean) {
    const document = documents.value.find((item) => item.path === path);
    if (document?.loaded) setProjectDocumentDirty(document, dirty);
  }

  /** `content` is what was written; edits typed while the write was in flight stay dirty. */
  function markDocumentSaved(path: string, content?: string) {
    const document = documents.value.find((item) => item.path === path);
    if (!document) return;
    document.savedContent = content ?? document.content;
    setProjectDocumentDirty(document, document.content !== document.savedContent);
  }

  function documentForSession(scope: string, path: string, sessionId: number | undefined) {
    const list = scope === activeWorkspaceId.value ? documents.value : runtimeDocuments.get(scope);
    return list?.find((item) => item.path === path && item.sessionId === sessionId);
  }

  /** A write may finish while its owning workspace is cached. Never acknowledge a reopened tab. */
  function acknowledgeDocumentSaved(scope: string, path: string, sessionId: number | undefined, content: string) {
    const document = documentForSession(scope, path, sessionId);
    if (!document) return;
    document.savedContent = content;
    setProjectDocumentDirty(document, document.content !== content);
  }

  function revealDocumentPosition(path: string, line?: number, column?: number) {
    const document = openDocument(path);
    document.revealPosition = line
      ? {
          line: Math.max(1, Math.floor(line)),
          column: column ? Math.max(1, Math.floor(column)) : undefined,
        }
      : undefined;
  }

  function revealDocumentLine(path: string, line?: number) {
    revealDocumentPosition(path, line);
  }

  function consumeRevealPosition(path: string): ProjectEditorRevealPosition | undefined {
    const document = documents.value.find((item) => item.path === path);
    if (!document) return undefined;
    const position = document.revealPosition;
    document.revealPosition = undefined;
    return position;
  }

  function consumeRevealLine(path: string) {
    return consumeRevealPosition(path)?.line;
  }

  function setEditorViewState(path: string, state: ProjectEditorViewState) {
    const sanitized = sanitizeEditorViewState(state);
    if (!sanitized || !documents.value.some((document) => document.path === path)) return;
    editorViewStates.value = { ...editorViewStates.value, [path]: sanitized };
  }

  function closeDocument(path: string) {
    const index = documents.value.findIndex((document) => document.path === path);
    if (index < 0) return;
    documents.value.splice(index, 1);
    delete editorViewStates.value[path];
    if (activePath.value !== path) return;
    activePath.value = documents.value[index]?.path ?? documents.value[index - 1]?.path ?? null;
  }

  function renameDocuments(oldPath: string, newPath: string, isDir: boolean) {
    flushDocumentContent();
    const prefix = `${oldPath.replace(/\/$/, "")}/`;
    for (const document of documents.value) {
      if (document.path === oldPath || (isDir && document.path.startsWith(prefix))) {
        const suffix = document.path.slice(oldPath.length);
        document.sessionId = ++nextDocumentSession;
        document.path = `${newPath}${suffix}`;
        document.name = projectBasename(document.path);
      }
    }
    const renamedViewStates: Record<string, ProjectEditorViewState> = {};
    for (const [path, state] of Object.entries(editorViewStates.value)) {
      const renamedPath =
        path === oldPath || (isDir && path.startsWith(prefix))
          ? `${newPath}${path.slice(oldPath.length)}`
          : path;
      renamedViewStates[renamedPath] = state;
    }
    editorViewStates.value = renamedViewStates;
    if (activePath.value === oldPath || (isDir && activePath.value?.startsWith(prefix))) {
      activePath.value = `${newPath}${activePath.value.slice(oldPath.length)}`;
    }
  }

  function removeDocuments(path: string, isDir: boolean) {
    const prefix = `${path.replace(/\/$/, "")}/`;
    const removedActive =
      activePath.value === path || Boolean(isDir && activePath.value?.startsWith(prefix));
    documents.value = documents.value.filter(
      (document) => document.path !== path && !(isDir && document.path.startsWith(prefix))
    );
    editorViewStates.value = Object.fromEntries(
      Object.entries(editorViewStates.value).filter(
        ([documentPath]) => documentPath !== path && !(isDir && documentPath.startsWith(prefix))
      )
    );
    if (removedActive) activePath.value = documents.value[0]?.path ?? null;
  }

  function updateEditorPreferences(patch: Partial<ProjectEditorPreferences>) {
    editorPreferences.value = sanitizeEditorPreferences({ ...editorPreferences.value, ...patch });
  }

  watch(
    [
      projectRoot,
      projects,
      openPaths,
      activePath,
      documentEditorKeys,
      editorPreferences,
      projectTerminalTabs,
      activeProjectTerminalId,
      lspSettings,
    ],
    schedulePersist,
    { deep: true }
  );
  // setEditorViewState replaces the map, so a shallow watch sees every update.
  watch(editorViewStates, scheduleViewStatePersist);
  onBeforeUnmount(() => void flushPersistence());

  function updateLspSettings(patch: Partial<LspProjectSettings>) {
    lspSettings.value = sanitizeLspSettings({ ...lspSettings.value, ...patch });
  }

  /**
   * 浏览器本地模式：各 `browser://` 项目根的文件夹访问状态。不持久化：刷新后浏览器会把权限重置为
   * `prompt`，需重新查询；`prompt` 时由用户点击「重新授权」恢复，`missing` 时只能重新选择文件夹。
   */
  const browserRootAccess = ref<Record<string, BrowserProjectAccess>>({});

  function setBrowserRootAccess(root: string, access: BrowserProjectAccess) {
    browserRootAccess.value = { ...browserRootAccess.value, [root]: access };
  }

  /** 查询并记录 `browser://` 项目根的访问状态；只在浏览器本地模式下调用，从不抛错。 */
  async function refreshBrowserRootAccess(root: string): Promise<BrowserProjectAccess> {
    const normalized = normalizeRoot(root);
    const access = await import("@/services/browser-runtime/project-fs")
      .then(({ browserProjectPermission }) => browserProjectPermission(normalized))
      .catch((): BrowserProjectAccess => "missing");
    setBrowserRootAccess(normalized, access);
    return access;
  }

  /** 请求文件夹读写权限；浏览器要求在用户点击中调用。授权成功返回 true，从不抛错。 */
  async function requestBrowserRootAccess(root: string) {
    const normalized = normalizeRoot(root);
    const granted = await import("@/services/browser-runtime/project-fs")
      .then(({ requestBrowserProjectPermission }) => requestBrowserProjectPermission(normalized))
      .catch(() => false);
    if (granted) setBrowserRootAccess(normalized, "granted");
    else await refreshBrowserRootAccess(normalized);
    return granted;
  }

  return {
    projectRoot,
    projects,
    documents,
    activePath,
    editorPreferences,
    editorViewStates,
    projectTerminalTabs,
    activeProjectTerminalId,
    lspSettings,
    activeWorkspaceId,
    hydrated,
    activeDocument,
    dirtyCount,
    currentProject,
    hydrate,
    switchWorkspace,
    projectRootForWorkspace,
    projectsForWorkspace,
    allListedRoots,
    rootsListedByOtherWorkspaces,
    rootsByOtherWorkspace,
    discardWorkspaceState,
    flushPersistence,
    resetProject,
    restoreProject,
    renameProject,
    removeProject,
    mergeProjects,
    openDocument,
    setDocumentLoading,
    setDocumentLoaded,
    setDocumentError,
    updateDocumentContent,
    setDocumentCustomEditor,
    setDocumentDirty,
    markDocumentSaved,
    documentForSession,
    acknowledgeDocumentSaved,
    registerDocumentContentFlusher,
    flushDocumentContent,
    revealDocumentPosition,
    revealDocumentLine,
    consumeRevealPosition,
    consumeRevealLine,
    setEditorViewState,
    closeDocument,
    renameDocuments,
    removeDocuments,
    updateEditorPreferences,
    updateLspSettings,
    browserRootAccess,
    refreshBrowserRootAccess,
    requestBrowserRootAccess,
  };
});

// Pinia store 实例会被缓存：模块热更新后 useStore 仍返回旧实例，
// 闭包里握着的还是旧函数，于是「代码已更新但行为没变」。
// 参见 https://pinia.vuejs.org/cookbook/hot-module-replacement.html
if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useLocalProjectStore, import.meta.hot));
}
