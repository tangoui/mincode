import { dashboardBasename } from "@/domain/dashboard/overview";
import { useVueI18n } from "@/i18n/messages";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";
import { syncSessionToolRoots } from "@/services/session-tool-preferences";

interface RecentSession {
  id?: string;
  uuid?: string;
  title?: string;
  cwd?: string;
  decodedPath?: string;
  lastAt?: number;
  messageCount?: number;
  model?: string;
}

interface ClaudeProject {
  encodedDir: string;
  decodedPath: string;
  sessionCount: number;
  lastAt: number;
}

export interface ActivityItem {
  id: string;
  provider: "Codex" | "Claude";
  title: string;
  path: string;
  lastAt: number;
  count: number;
}

async function fetchRecentDashboardActivity(): Promise<ActivityItem[]> {
  if (!isDesktopRuntime()) return [];
  const { t } = useVueI18n();
  await syncSessionToolRoots();
  const [codexSessions, claudeProjects] = await Promise.all([
    invokeBackend<RecentSession[]>("list_codex_sessions", {}).catch(() => []),
    invokeBackend<ClaudeProject[]>("list_claude_projects", {}).catch(() => []),
  ]);
  return [
    ...codexSessions.map(
      (session, index): ActivityItem => ({
        id: `codex:${session.uuid ?? session.id ?? index}`,
        provider: "Codex",
        title:
          session.title?.trim() || dashboardBasename(session.cwd ?? "") || t("dashboard.sessions.untitled"),
        path: session.cwd ?? session.decodedPath ?? "",
        lastAt: session.lastAt ?? 0,
        count: session.messageCount ?? 0,
      })
    ),
    ...claudeProjects.map(
      (item): ActivityItem => ({
        id: `claude:${item.encodedDir}`,
        provider: "Claude",
        title: dashboardBasename(item.decodedPath) || item.decodedPath,
        path: item.decodedPath,
        lastAt: item.lastAt,
        count: item.sessionCount,
      })
    ),
  ]
    .sort((left, right) => right.lastAt - left.lastAt)
    .slice(0, 5);
}

let pendingRecent: Promise<ActivityItem[]> | undefined;

export function loadRecentDashboardActivity() {
  // An overview tool and the visible dashboard may request the same host scan together.
  pendingRecent ??= fetchRecentDashboardActivity().finally(() => { pendingRecent = undefined; });
  return pendingRecent;
}
