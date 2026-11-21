import assert from "node:assert/strict";
import test from "node:test";

import {
  findOrphanedSshConfigProfiles,
  mergeSshConfigProfile,
  resolveSshConfigProxyJump,
} from "../src/domain/ssh/types.ts";

const bastion = {
  alias: "bastion",
  host: "jump.internal",
  port: 2222,
  username: "jump-user",
  identityFile: "/keys/jump",
  sourcePath: "/home/test/.ssh/config",
};
const app = {
  alias: "app",
  host: "app.internal",
  port: 22,
  username: "deploy",
  proxyJump: "bastion",
  serverAliveInterval: 20,
  sourcePath: "/home/test/.ssh/config",
};

test("resolves ProxyJump aliases through the discovered config", () => {
  assert.deepEqual(resolveSshConfigProxyJump("bastion", [bastion, app], "deploy"), {
    host: "jump.internal",
    port: 2222,
    username: "jump-user",
    authType: "key",
    keyFile: "/keys/jump",
  });
});

test("merges managed SSH Config fields while preserving user metadata", () => {
  const existing = {
    id: "profile-app",
    name: "Production App",
    host: "old.internal",
    port: 22,
    username: "old-user",
    authType: "agent",
    group: "Production",
    tags: ["critical"],
    favorite: true,
    saveCredentials: true,
    portForwards: [],
    keepAlive: { intervalSeconds: 30, maxMisses: 5 },
    reconnect: { enabled: true, maxAttempts: 4, baseDelayMs: 500, maxDelayMs: 8000 },
    createdAt: 1,
    updatedAt: 2,
  };
  const merged = mergeSshConfigProfile(
    app,
    [bastion, app],
    existing,
    "/home/test/.ssh/config",
    100
  );
  assert.equal(merged.id, "profile-app");
  assert.equal(merged.name, "Production App");
  assert.equal(merged.host, "app.internal");
  assert.equal(merged.username, "deploy");
  assert.equal(merged.group, "Production");
  assert.deepEqual(merged.tags, ["critical"]);
  assert.equal(merged.keepAlive.intervalSeconds, 20);
  assert.equal(merged.keepAlive.maxMisses, 5);
  assert.equal(merged.jumpHost.host, "jump.internal");
  assert.equal(merged.sshConfigSource.rootPath, "/home/test/.ssh/config");
});

test("detects removed entries only within the selected root config", () => {
  const managed = mergeSshConfigProfile(
    { ...app, alias: "removed" },
    [bastion, app],
    undefined,
    "/home/test/.ssh/config",
    100
  );
  const unrelated = {
    ...managed,
    id: "other",
    sshConfigSource: { ...managed.sshConfigSource, rootPath: "/other/config" },
  };
  assert.deepEqual(
    findOrphanedSshConfigProfiles([managed, unrelated], [app], "/home/test/.ssh/config").map(
      (profile) => profile.id
    ),
    [managed.id]
  );
});
