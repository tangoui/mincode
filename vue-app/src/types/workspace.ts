export const workspaceViews = [
 "dashboard",
 "terminal",
 "ssh",
 "database",
 "sftp",
 "http",
 "notes",
 "sessions",
 "plugins",
 "plugin-view",
 "service-result",
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
 pluginView?: {
  pluginId: string;
  viewId: string;
 };
 /**
  * 插件服务结果的只读标签（services/plugin-service-result-tabs.ts）：内容只在内存里，
  * 关闭即丢弃，不写入会话存储，重启后也不恢复。
  */
 serviceResult?: {
  resultId: string;
 };
}
