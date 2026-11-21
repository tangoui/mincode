import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

const MAX_CWD_LENGTH = 32_768;

function normalizeCwd(value: unknown) {
  if (typeof value !== "string") return undefined;
  const cwd = value.trim();
  return cwd && cwd.length <= MAX_CWD_LENGTH ? cwd : undefined;
}

export async function loadLocalShellCwds(
  sessionIds: readonly string[]
): Promise<Record<string, string>> {
  if (!isDesktopRuntime()) return {};

  const uniqueIds = [...new Set(sessionIds.map((id) => id.trim()).filter(Boolean))];
  const entries = await Promise.all(
    uniqueIds.map(async (sessionId) => {
      try {
        const cwd = normalizeCwd(
          await invokeBackend<string>("get_local_shell_cwd", { payload: { sessionId } })
        );
        return cwd ? ([sessionId, cwd] as const) : undefined;
      } catch {
        // A pane may not have mounted yet or its PTY may already have exited.
        return undefined;
      }
    })
  );

  return Object.fromEntries(entries.filter((entry): entry is readonly [string, string] => !!entry));
}
