/**
 * The editor that owns nested document tabs inside the active workbench tab
 * (today: the project workspace's document/terminal tabs). The workbench tab
 * commands (composables/use-workbench-tab-commands.ts) route `close-tab`,
 * `close-all`, `next-tab`/`prev-tab`/`tab-N` and `save-all` to it while the focus is inside its editor area.
 *
 * A host registers while its view is active (onMounted/onActivated) and
 * disposes on deactivate/unmount. The latest registration wins.
 */

export interface DocumentTabHost {
  /** Whether `element` lies inside the host's editor area. */
  contains(element: Element): boolean;
  /** Ordered document tab ids, and the active one. */
  tabIds(): string[];
  activeTabId(): string;
  selectTab(id: string): void;
  /** Closes a tab with the same unsaved-changes prompt as its close button. */
  closeTab(id: string): Promise<void>;
  /** Closes every document; asks once when any of them has unsaved changes. */
  closeAll(): Promise<void>;
  saveAll(): Promise<void>;
}

let current: { host: DocumentTabHost; token: symbol } | null = null;

export function registerDocumentTabHost(host: DocumentTabHost): () => void {
  const token = Symbol("document-tab-host");
  current = { host, token };
  return () => {
    if (current?.token === token) current = null;
  };
}

export function activeDocumentTabHost(): DocumentTabHost | undefined {
  return current?.host;
}
