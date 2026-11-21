import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const terminalSource = await readFile(
  new URL("../src/components/ssh/SshDockerTerminal.vue", import.meta.url),
  "utf8"
);
const managerSource = await readFile(
  new URL("../src/components/ssh/SshServerManagerPanel.vue", import.meta.url),
  "utf8"
);
const sshTerminalSource = await readFile(
  new URL("../src/components/terminal/SshTerminal.vue", import.meta.url),
  "utf8"
);
const serviceSource = await readFile(
  new URL("../src/services/ssh-docker-service.ts", import.meta.url),
  "utf8"
);

test("Docker terminal stays lightweight and keeps logs read-only", () => {
  assert.doesNotMatch(terminalSource, /SerializeAddon|SearchAddon|saveTerminalSnapshot/);
  assert.match(terminalSource, /if \(props\.mode === "exec"\) \{\s*inputSubscription = xterm\.onData/s);
  assert.match(terminalSource, /disableStdin: props\.mode === "logs"/);
});

test("server manager owns Docker management and embeds stream terminals", () => {
  assert.match(managerSource, /loadSshDockerInfo/);
  assert.match(managerSource, /loadSshDockerContainers/);
  assert.match(managerSource, /runSshDockerAction/);
  assert.match(managerSource, /<SshDockerTerminal/);
  assert.match(managerSource, /openDockerTerminal\(row, 'exec'\)/);
  assert.match(managerSource, /openDockerTerminal\(row, 'logs'\)/);
});

test("SSH terminal no longer mounts a standalone Docker panel", () => {
  assert.doesNotMatch(sshTerminalSource, /SshDockerPanel|dockerPanelVisible|ssh\.docker\./);
});

test("Docker stream service uses dedicated start commands and generic shell IO", () => {
  assert.match(serviceSource, /start_ssh_docker_exec/);
  assert.match(serviceSource, /start_ssh_docker_log_stream/);
  assert.match(serviceSource, /write_to_shell/);
  assert.match(serviceSource, /resize_local_shell/);
  assert.match(serviceSource, /close_local_shell/);
});
