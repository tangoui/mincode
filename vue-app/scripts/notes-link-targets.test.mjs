import test from "node:test";
import assert from "node:assert/strict";
import { resolveLink } from "@/domain/notes/link-targets";

function snapshot(overrides = {}) {
  return {
    ssh: new Map([["ssh-1", "prod web"]]),
    database: new Map([["db-1", "orders"]]),
    http: new Map([["req-1", "List users"]]),
    httpCollections: new Map([["req-1", "col-1"]]),
    currentProjects: new Map([["/repo/current", "current"]]),
    otherWorkspaceProjects: new Map([["scope-b", ["/repo/b-only"]]]),
    currentAi: new Map([["conv-1", "Refactor plan"]]),
    currentScopeId: "scope-a",
    liveScopeIds: new Set(["scope-a", "scope-b"]),
    workspaceNames: new Map([["ws-a", "Alpha"], ["scope-a", "Alpha"], ["ws-b", "Beta"], ["scope-b", "Beta"]]),
    unavailable: new Set(),
    ...overrides,
  };
}

function link(objectType, objectId, extra = {}) {
  return { objectType, objectId, parentId: "", scopeId: "", nameSnapshot: "", scopeNameSnapshot: "", createdAt: 1, ...extra };
}

test("marks links to missing objects as deleted", () => {
  assert.deepEqual(resolveLink(link("ssh", "ssh-gone", { nameSnapshot: "old box" }), snapshot()), {
    status: "deleted",
    name: "old box",
  });
  assert.equal(resolveLink(link("database", "db-gone", { nameSnapshot: "x" }), snapshot()).status, "deleted");
  assert.equal(resolveLink(link("http", "req-gone", { nameSnapshot: "x" }), snapshot()).status, "deleted");
  assert.deepEqual(resolveLink(link("project", "/repo/nowhere", { nameSnapshot: "nowhere" }), snapshot()), {
    status: "deleted",
    name: "nowhere",
  });
});

test("global objects that still exist are current and use the live name", () => {
  assert.deepEqual(resolveLink(link("ssh", "ssh-1", { nameSnapshot: "renamed away" }), snapshot()), {
    status: "current",
    name: "prod web",
  });
  assert.deepEqual(resolveLink(link("database", "db-1"), snapshot()), { status: "current", name: "orders" });
  assert.deepEqual(resolveLink(link("http", "req-1", { parentId: "col-old" }), snapshot()), {
    status: "current",
    name: "List users",
  });
});

test("projects resolve against the current workspace first, then other workspaces", () => {
  assert.deepEqual(resolveLink(link("project", "/repo/current"), snapshot()), { status: "current", name: "current" });
  assert.deepEqual(resolveLink(link("project", "/repo/b-only", { nameSnapshot: "b-only" }), snapshot()), {
    status: "otherWorkspace",
    name: "b-only",
    workspaceName: "Beta",
  });
});

test("AI conversations resolve by scope", () => {
  const current = resolveLink(link("ai", "conv-1", { scopeId: "scope-a", nameSnapshot: "old" }), snapshot());
  assert.deepEqual(current, { status: "current", name: "Refactor plan" });

  const missingHere = resolveLink(link("ai", "conv-gone", { scopeId: "scope-a", nameSnapshot: "gone" }), snapshot());
  assert.deepEqual(missingHere, { status: "deleted", name: "gone" });

  // Other workspaces' conversations are not loaded: a live workspace means "other workspace".
  const elsewhere = resolveLink(link("ai", "conv-9", { scopeId: "scope-b", nameSnapshot: "B chat", scopeNameSnapshot: "Old B" }), snapshot());
  assert.deepEqual(elsewhere, { status: "otherWorkspace", name: "B chat", workspaceName: "Beta" });

  const workspaceGone = resolveLink(link("ai", "conv-9", { scopeId: "scope-z", nameSnapshot: "Z chat", scopeNameSnapshot: "Zeta" }), snapshot());
  assert.deepEqual(workspaceGone, { status: "deleted", name: "Z chat" });
});

test("an AI link whose conversation moved into the current workspace is current", () => {
  // The stored scope is stale after the conversation was moved; the current workspace has it now.
  assert.deepEqual(
    resolveLink(link("ai", "conv-1", { scopeId: "scope-b", nameSnapshot: "old", scopeNameSnapshot: "Beta" }), snapshot()),
    { status: "current", name: "Refactor plan" },
  );
  assert.deepEqual(
    resolveLink(link("ai", "conv-1", { scopeId: "scope-z", nameSnapshot: "old" }), snapshot()),
    { status: "current", name: "Refactor plan" },
  );
});

test("an AI link without a scope belongs to the current workspace", () => {
  assert.equal(resolveLink(link("ai", "conv-1"), snapshot()).status, "current");
});

test("other-workspace names fall back to the scope name snapshot", () => {
  const resolved = resolveLink(
    link("ai", "conv-9", { scopeId: "scope-b", nameSnapshot: "B chat", scopeNameSnapshot: "Old B" }),
    snapshot({ workspaceNames: new Map() }),
  );
  assert.equal(resolved.workspaceName, "Old B");
});

test("a live title that is empty falls back to the name snapshot", () => {
  const resolved = resolveLink(
    link("ai", "conv-1", { scopeId: "scope-a", nameSnapshot: "first prompt" }),
    snapshot({ currentAi: new Map([["conv-1", ""]]) }),
  );
  assert.deepEqual(resolved, { status: "current", name: "first prompt" });
});

test("objects whose source failed or is still loading are unknown, not deleted", () => {
  const unavailable = snapshot({ unavailable: new Set(["ssh", "project", "ai"]) });
  assert.deepEqual(resolveLink(link("ssh", "ssh-gone", { nameSnapshot: "old box" }), unavailable), {
    status: "unknown",
    name: "old box",
  });
  // An object that is present still resolves normally.
  assert.equal(resolveLink(link("ssh", "ssh-1"), unavailable).status, "current");
  assert.equal(resolveLink(link("project", "/repo/nowhere", { nameSnapshot: "n" }), unavailable).status, "unknown");
  assert.equal(resolveLink(link("project", "/repo/b-only"), unavailable).status, "otherWorkspace");
  assert.equal(resolveLink(link("ai", "conv-gone", { scopeId: "scope-a" }), unavailable).status, "unknown");
  // The workspace list is always known: a removed workspace stays deleted.
  assert.equal(resolveLink(link("ai", "conv-9", { scopeId: "scope-z" }), unavailable).status, "deleted");
  // Other types are unaffected.
  assert.equal(resolveLink(link("database", "db-gone"), unavailable).status, "deleted");
});
