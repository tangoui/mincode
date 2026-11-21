import { parseSshHistoryData, type SshHistoryRequest } from "@/domain/ssh/command-history";
import { invokeBackend } from "@/services/desktop-bridge";

export interface SshHistoryTarget {
  sessionId: string;
  profileId: string;
  host: string;
  port: number;
  username: string;
}

export async function requestSshHistory(target: SshHistoryTarget, request: SshHistoryRequest) {
  const { action, ...payload } = request;
  return parseSshHistoryData(await invokeBackend<unknown>(`ssh_history_${action}`, { ...target, ...payload }));
}
