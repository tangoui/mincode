import type { AiCapability } from "./capability-registry";

export interface AiSelectableTool {
  name: string;
  module: string;
  discovery?: boolean;
}

const computerUseContextKeywords = [
  "computer use",
  "desktop",
  "screen",
  "mouse",
  "keyboard",
  "macos",
  "mac os",
  "on my computer",
  "on the computer",
  "local app",
  "native app",
  "desktop app",
  "桌面",
  "屏幕",
  "鼠标",
  "键盘",
  "操作电脑",
  "操作应用",
  "电脑上",
  "电脑里的",
  "本机",
  "本地应用",
  "桌面应用",
  "应用程序",
] as const;

const desktopApplicationKeywords = [
  "google chrome",
  "chrome",
  "safari",
  "firefox",
  "microsoft edge",
  "finder",
  "谷歌浏览器",
  "谷歌流览器",
  "火狐浏览器",
  "访达",
] as const;

const desktopActionKeywords = [
  "open",
  "launch",
  "start",
  "activate",
  "switch to",
  "打开",
  "启动",
  "运行",
  "激活",
  "切换到",
] as const;

export const DEFAULT_AI_TOOL_MODULE_KEYWORDS: Readonly<
  Record<string, readonly string[]>
> = {
  workspace: [
    "workspace",
    "project",
    "file",
    "code",
    "工作区",
    "项目",
    "文件",
    "代码",
    "页面",
    "标签",
    "导航",
    "打开",
    "切换",
    "跳转",
    "聚焦",
    "显示",
    "左侧菜单",
  ],
  git: ["git", "commit", "branch", "diff", "仓库", "提交", "分支"],
  database: ["database", "sql", "query", "数据库", "查询"],
  redis: ["redis", "cache", "缓存"],
  http: ["http", "api", "request", "接口", "请求"],
  browser: ["browser", "webpage", "浏览器", "网页"],
  computer: computerUseContextKeywords,
  terminal: ["terminal", "shell", "command", "终端", "命令"],
  ssh: ["ssh", "remote", "远程"],
  sftp: ["sftp", "upload", "download", "上传", "下载"],
  plugin: ["plugin", "extension", "插件", "扩展"],
  devtools: [
    "devtools",
    "developer tool",
    "base64",
    "json",
    "regex",
    "开发工具",
    "编码",
    "解码",
    "格式化",
  ],
  sessions: ["session", "claude", "codex", "会话", "对话记录"],
  settings: [
    "settings",
    "config",
    "update",
    "version",
    "mcp",
    "设置",
    "配置",
    "更新",
    "版本",
  ],
  system: ["system", "os", "port", "process", "系统", "端口", "进程"],
};

interface CapabilitySelectionRequest {
  text: string;
  currentModule?: string;
  availableModules: readonly string[];
  historicalModules?: readonly string[];
}

export interface AiToolSelectionRequest {
  text: string;
  currentModule?: string;
  availableModules: readonly string[];
  historicalModules?: readonly string[];
  moduleKeywords?: Readonly<Record<string, readonly string[]>>;
}

const workspaceNavigationKeywords = [
  "open",
  "switch",
  "navigate",
  "go to",
  "focus",
  "show",
  "page",
  "tab",
  "sidebar",
  "打开",
  "切换",
  "跳转",
  "进入",
  "聚焦",
  "显示",
  "页面",
  "标签",
  "左侧菜单",
  "首页",
] as const;

export function isWorkspaceNavigationIntent(text: string) {
  return matchesKeyword(text, workspaceNavigationKeywords);
}

export function isComputerUseIntent(text: string) {
  if (matchesKeyword(text, computerUseContextKeywords)) return true;
  return (
    matchesKeyword(text, desktopActionKeywords) &&
    matchesKeyword(text, desktopApplicationKeywords)
  );
}

const directConversationPatterns = [
  /^(?:你好|您好|嗨|哈喽|在吗|早上好|下午好|晚上好|晚安)(?:呀|啊|哦|呢|啦|！|!|。|\s)*$/u,
  /^(?:谢谢|多谢|感谢|好的|好|收到|明白了|知道了)(?:你|您)?(?:呀|啊|哦|呢|啦|！|!|。|\s)*$/u,
  /^(?:hi|hello|hey)(?:\s+there)?[!.?\s]*$/i,
] as const;

/** Short social turns should not pay for or expose unrelated tool schemas. */
export function isDirectConversationIntent(text: string) {
  const normalized = text.trim();
  return normalized.length > 0 && directConversationPatterns.some((pattern) => pattern.test(normalized));
}

function matchesKeyword(text: string, keywords: readonly string[]) {
  const normalizedText = text.toLocaleLowerCase();
  return keywords.some((keyword) => {
    const normalizedKeyword = keyword.trim().toLocaleLowerCase();
    return (
      normalizedKeyword.length > 0 && normalizedText.includes(normalizedKeyword)
    );
  });
}

export function selectAiTools<T extends AiSelectableTool>(
  registeredTools: readonly T[],
  request: AiToolSelectionRequest,
): T[] {
  if (isDirectConversationIntent(request.text)) return [];
  const availableModules = new Set(request.availableModules);
  const selectedModules = new Set<string>();

  if (request.currentModule && availableModules.has(request.currentModule)) {
    selectedModules.add(request.currentModule);
  }
  for (const module of request.historicalModules ?? []) {
    if (availableModules.has(module)) selectedModules.add(module);
  }
  for (const [module, keywords] of Object.entries(
    request.moduleKeywords ?? {},
  )) {
    if (
      availableModules.has(module) &&
      matchesKeyword(request.text, keywords)
    ) {
      selectedModules.add(module);
    }
  }
  if (availableModules.has("computer") && isComputerUseIntent(request.text)) {
    selectedModules.add("computer");
  }

  const navigationIntent = isWorkspaceNavigationIntent(request.text);
  return registeredTools.filter(
    (tool) =>
      availableModules.has(tool.module) &&
      (tool.discovery === true ||
        selectedModules.has(tool.module) ||
        (tool.name === "workspace.open_view" && navigationIntent)),
  );
}

export function availableAiCapabilityModules(
  capabilities: readonly Pick<AiCapability, "module" | "target" | "tool">[],
  listResources: (capability?: string) => readonly unknown[],
) {
  const modules = new Set<string>();
  for (const capability of capabilities) {
    if (
      capability.target === "none" ||
      listResources(capability.tool.name).length > 0
    ) {
      modules.add(capability.module);
    }
  }
  return [...modules];
}

export function selectAiCapabilityTools(
  capabilities: readonly Pick<AiCapability, "module" | "discovery" | "tool">[],
  request: CapabilitySelectionRequest,
) {
  const selectable = capabilities.map((capability) => ({
    ...capability.tool,
    module: capability.module,
    discovery: capability.discovery,
  }));
  return selectAiTools(selectable, {
    ...request,
    moduleKeywords: DEFAULT_AI_TOOL_MODULE_KEYWORDS,
  }).map(({ module: _module, discovery: _discovery, ...tool }) => tool);
}
