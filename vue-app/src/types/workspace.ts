export const workspaceViews = [
 "dashboard",
 "terminal",
 "ssh",
 "database",
 "sftp",
 "http",
 "notes",
 "sessions",
 "settings",
 "ai",
] as const;

export type WorkspaceView = (typeof workspaceViews)[number];

export interface WorkspaceDefinition {
 id: string;
 name: string;
 createdAt: number;
 updatedAt: number;
}

export interface WorkspaceTab {
 id: string;
 workspaceId: string;
 view: WorkspaceView;
 titleKey: string;
 title?: string;
 closable: boolean;
 pinned?: boolean;
}
