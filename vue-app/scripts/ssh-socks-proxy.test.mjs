import assert from "node:assert/strict";
import test from "node:test";

import { sanitizeSshProfile } from "../src/domain/ssh/types.ts";

function baseProfile(overrides = {}) {
  return {
    id: "profile-socks",
    name: "socks",
    host: "app.internal",
    port: 22,
    username: "deploy",
    authType: "password",
    tags: [],
    favorite: false,
    saveCredentials: true,
    portForwards: [],
    createdAt: 1,
    updatedAt: 1,
    ...overrides,
  };
}

test("keeps a complete SOCKS5 proxy with trimmed host and default port", () => {
  const profile = sanitizeSshProfile(
    baseProfile({
      socksProxy: { host: " 127.0.0.1 ", port: undefined, username: " proxy-user " },
    })
  );
  assert.deepEqual(profile.socksProxy, {
    host: "127.0.0.1",
    port: 1080,
    username: "proxy-user",
  });
});

test("drops the proxy when the address is empty instead of persisting a half config", () => {
  const profile = sanitizeSshProfile(
    baseProfile({ socksProxy: { host: "   ", port: 1080, username: "user" } })
  );
  assert.equal(profile.socksProxy, undefined);
});

test("falls back to port 1080 when the proxy port is invalid", () => {
  const profile = sanitizeSshProfile(
    baseProfile({ socksProxy: { host: "proxy.internal", port: 99999 } })
  );
  assert.equal(profile.socksProxy?.port, 1080);
});

test("profiles without a proxy stay direct", () => {
  const profile = sanitizeSshProfile(baseProfile());
  assert.equal(profile.socksProxy, undefined);
});
