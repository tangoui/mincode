import { invokeBackend } from "@/services/desktop-bridge";

export type SshDockerTerminalMode = "exec" | "logs";

export interface StartSshDockerExecOptions {
  parentSessionId: string;
  sessionId: string;
  containerId: string;
  shell: string;
  cols: number;
  rows: number;
}

export interface StartSshDockerLogStreamOptions {
  parentSessionId: string;
  sessionId: string;
  containerId: string;
  tail?: number;
}

export function startSshDockerExec(options: StartSshDockerExecOptions) {
  return invokeBackend<void>("start_ssh_docker_exec", { ...options });
}

export function startSshDockerLogStream(options: StartSshDockerLogStreamOptions) {
  return invokeBackend<void>("start_ssh_docker_log_stream", { ...options });
}

export function writeToSshDockerExec(sessionId: string, data: string) {
  return invokeBackend<void>("write_to_shell", { payload: { sessionId, input: data } });
}

export function resizeSshDockerExec(sessionId: string, cols: number, rows: number) {
  return invokeBackend<void>("resize_local_shell", { payload: { sessionId, cols, rows } });
}

export function stopSshDockerStream(sessionId: string) {
  return invokeBackend<void>("close_local_shell", { payload: { sessionId } });
}
