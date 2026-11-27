import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

interface TerminalResumeRecord {
  command: string;
  cwd: string;
}

let resumeRecords: Promise<Map<string, TerminalResumeRecord>> | undefined;

export async function takeLocalShellResume(sessionId: string) {
  if (!isDesktopRuntime()) return undefined;
  // The native call removes the snapshot. Share its result between panes that
  // mount concurrently, and hand out each entry once even across pane reloads.
  resumeRecords ??= invokeBackend<Record<string, TerminalResumeRecord> | null>("take_local_shell_resume")
    .then((records) => new Map(Object.entries(records ?? {})))
    .catch(() => new Map<string, TerminalResumeRecord>());
  const records = await resumeRecords;
  const record = records.get(sessionId);
  records.delete(sessionId);
  return record;
}
