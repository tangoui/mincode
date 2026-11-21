import assert from "node:assert/strict";
import test from "node:test";
import {
  connectionConfigHasSecrets,
  parseConnectionConfig,
  portableConnection,
} from "../src/domain/database/connection-portability.ts";
import {
  buildSshProfileExport,
  parseSshCredentials,
  sshProfileImportHasSecrets,
} from "../src/domain/ssh/profile-portability.ts";
import { parseSshProfileImportBundle } from "../src/domain/ssh/import-profiles.ts";

function connection(overrides = {}) {
  return {
    id: "conn-1",
    name: "prod",
    dbType: "mysql",
    host: "10.0.0.1",
    port: 3306,
    username: "root",
    password: "s3cret",
    token: "tok",
    savePassword: true,
    ssl: false,
    tags: [],
    createdAt: 1,
    updatedAt: 1,
    tunnel: {
      type: "ssh",
      host: "bastion",
      port: 22,
      username: "ops",
      authType: "password",
      password: "jump",
      passphrase: "phrase",
    },
    ...overrides,
  };
}

test("database export strips every secret by default", () => {
  const portable = portableConnection(connection());
  assert.equal(portable.password, undefined);
  assert.equal(portable.token, undefined);
  assert.equal(portable.savePassword, false);
  assert.equal(portable.tunnel.password, undefined);
  assert.equal(portable.tunnel.passphrase, undefined);
  assert.equal(portable.host, "10.0.0.1");
});

test("database export carries plaintext secrets when asked", () => {
  const portable = portableConnection(connection(), { includeSecrets: true });
  assert.equal(portable.password, "s3cret");
  assert.equal(portable.token, "tok");
  assert.equal(portable.savePassword, true);
  assert.equal(portable.tunnel.password, "jump");
  assert.equal(portable.tunnel.passphrase, "phrase");
});

test("a database export file is recognised as carrying secrets", () => {
  const withSecrets = { connections: [portableConnection(connection(), { includeSecrets: true })] };
  const withoutSecrets = { connections: [portableConnection(connection())] };
  assert.equal(connectionConfigHasSecrets(withSecrets), true);
  assert.equal(connectionConfigHasSecrets(withoutSecrets), false);
  assert.equal(connectionConfigHasSecrets(null), false);
});

test("database import restores secrets only when the file carries them", () => {
  const file = { connections: [portableConnection(connection(), { includeSecrets: true })] };
  const [restored] = parseConnectionConfig(file, "invalid", { includeSecrets: true });
  assert.equal(restored.password, "s3cret");
  assert.equal(restored.tunnel.passphrase, "phrase");
  assert.equal(restored.savePassword, true);

  const [stripped] = parseConnectionConfig(file, "invalid");
  assert.equal(stripped.password, undefined);
  // 不带口令时必须关掉保存开关，否则导入会用空口令覆盖本机已存的凭据。
  assert.equal(stripped.savePassword, false);
});

test("database import rejects a malformed file", () => {
  assert.throws(() => parseConnectionConfig({ connections: [{}] }, "invalid"), /invalid/);
  assert.throws(() => parseConnectionConfig("nope", "invalid"), /invalid/);
});

const sshProfile = {
  id: "ssh-1",
  name: "prod",
  host: "10.0.0.2",
  port: 22,
  username: "ops",
  authType: "password",
  createdAt: 1,
  updatedAt: 1,
};

test("ssh export omits credentials unless secrets are requested", () => {
  const credentials = { password: "p", passphrase: "k", totpUri: "otpauth://x" };
  const bare = buildSshProfileExport([sshProfile], () => credentials, {
    includeSecrets: false,
    exportedAt: "2026-01-01T00:00:00.000Z",
  });
  assert.equal(bare.includesSecrets, false);
  assert.equal(bare.profiles[0].credentials, undefined);
  assert.equal(sshProfileImportHasSecrets(bare), false);

  const full = buildSshProfileExport([sshProfile], () => credentials, {
    includeSecrets: true,
    exportedAt: "2026-01-01T00:00:00.000Z",
  });
  assert.equal(full.includesSecrets, true);
  assert.deepEqual(full.profiles[0].credentials, credentials);
  assert.equal(sshProfileImportHasSecrets(full), true);
});

test("ssh credential parsing drops empty and machine-bound values", () => {
  assert.equal(parseSshCredentials({ password: "" }), undefined);
  assert.equal(parseSshCredentials({ password: "enc:v2:abc" }), undefined);
  assert.equal(parseSshCredentials("nope"), undefined);
  assert.deepEqual(parseSshCredentials({ password: "p", unknown: "x" }), { password: "p" });
});

test("ssh import returns each profile with its plaintext credentials", () => {
  const payload = buildSshProfileExport([sshProfile], () => ({ password: "p" }), {
    includeSecrets: true,
    exportedAt: "2026-01-01T00:00:00.000Z",
  });
  const imported = parseSshProfileImportBundle(payload, 42);
  assert.equal(imported.length, 1);
  assert.equal(imported[0].profile.host, "10.0.0.2");
  assert.deepEqual(imported[0].credentials, { password: "p" });

  const withoutSecrets = buildSshProfileExport([sshProfile], () => ({ password: "p" }), {
    includeSecrets: false,
    exportedAt: "2026-01-01T00:00:00.000Z",
  });
  assert.equal(parseSshProfileImportBundle(withoutSecrets, 42)[0].credentials, undefined);
});
