import assert from "node:assert/strict";
import test from "node:test";

const { tunnelSummary, extraSummary, initialOpenSections } = await import(
  "../src/domain/database/connection-form-layout.ts"
);

const ssh = {
  type: "ssh",
  host: "jump.example.com",
  port: 22,
  username: "ops",
  authType: "key",
  keyPath: "~/.ssh/id_rsa",
};

test("tunnelSummary: no tunnel is null", () => {
  assert.equal(tunnelSummary(undefined), null);
});

test("tunnelSummary: type label and host", () => {
  assert.deepEqual(tunnelSummary(ssh), { type: "SSH", host: "jump.example.com" });
  assert.deepEqual(tunnelSummary({ type: "socks5", host: "proxy.local", port: 1080 }), {
    type: "SOCKS5",
    host: "proxy.local",
  });
  assert.deepEqual(tunnelSummary({ type: "http_connect", host: "gw.local", port: 8080 }), {
    type: "HTTP CONNECT",
    host: "gw.local",
  });
});

test("tunnelSummary: tolerates malformed imported data", () => {
  assert.deepEqual(tunnelSummary({ type: "weird", host: 42, port: 1 }), { type: "weird", host: "" });
});

test("extraSummary: group then tags, description not included", () => {
  assert.deepEqual(extraSummary({ group: "生产", tags: ["a", "b"] }), {
    items: ["生产", "a", "b"],
    hasDescription: false,
  });
  assert.deepEqual(extraSummary({ group: "生产", tags: ["a"], description: "主库" }), {
    items: ["生产", "a"],
    hasDescription: true,
  });
});

test("extraSummary: empty values are dropped", () => {
  assert.deepEqual(extraSummary({}), { items: [], hasDescription: false });
  assert.deepEqual(extraSummary({ group: "  ", tags: ["", " "], description: "  " }), {
    items: [],
    hasDescription: false,
  });
});

test("extraSummary: description only", () => {
  assert.deepEqual(extraSummary({ description: "备注" }), { items: [], hasDescription: true });
});

test("initialOpenSections: opens the sections that have content", () => {
  assert.deepEqual(initialOpenSections({}), []);
  assert.deepEqual(initialOpenSections({ tunnel: ssh }), ["tunnel"]);
  assert.deepEqual(initialOpenSections({ tags: ["a"] }), ["extra"]);
  assert.deepEqual(initialOpenSections({ group: "生产" }), ["extra"]);
  assert.deepEqual(initialOpenSections({ description: "备注" }), ["extra"]);
  assert.deepEqual(initialOpenSections({ tunnel: ssh, group: "生产" }), ["tunnel", "extra"]);
});
