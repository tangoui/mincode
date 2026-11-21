import assert from "node:assert/strict";
import test from "node:test";
import {
  filterHttpHistory,
  groupHttpHistory,
  historyDisplayName,
  historyStatusTone,
  historyUrlSummary,
} from "../src/domain/http/history.ts";

test("summarizes history URLs and derives display names", () => {
  assert.equal(historyUrlSummary("https://www.example.com/index.html#/community"), "www.example.com/index.html");
  assert.equal(historyUrlSummary("https://api.example.com/?a=1"), "api.example.com");
  assert.equal(historyUrlSummary("{{baseUrl}}/users/42?x=1"), "{{baseUrl}}/users/42");
  assert.equal(historyDisplayName({ name: "用户详情", url: "https://x/users/1" }, "Request"), "用户详情");
  assert.equal(historyDisplayName({ url: "https://x/users/1" }, "Request"), "1");
  assert.equal(historyDisplayName({ name: "https://x/a", url: "https://x/a" }, "Request"), "a");
  assert.equal(historyDisplayName({ url: "https://api.example.com" }, "Request"), "api.example.com");
  assert.equal(historyDisplayName({ url: "" }, "Request"), "Request");
});

test("maps status codes to tones", () => {
  assert.equal(historyStatusTone(204), "ok");
  assert.equal(historyStatusTone(302), "redirect");
  assert.equal(historyStatusTone(404), "client-error");
  assert.equal(historyStatusTone(502), "server-error");
  assert.equal(historyStatusTone(0), "server-error");
});

test("groups history by local day while keeping order", () => {
  const now = new Date(2026, 9, 29, 15, 0).getTime();
  const at = (day, hour) => new Date(2026, 9, day, hour).getTime();
  const items = [
    { id: "a", method: "GET", url: "u", status: 200, elapsed: 1, createdAt: at(29, 14) },
    { id: "b", method: "GET", url: "u", status: 200, elapsed: 1, createdAt: at(29, 0) },
    { id: "c", method: "GET", url: "u", status: 200, elapsed: 1, createdAt: at(28, 23) },
    { id: "d", method: "GET", url: "u", status: 200, elapsed: 1, createdAt: at(20, 9) },
    { id: "e", method: "GET", url: "u", status: 200, elapsed: 1, createdAt: at(20, 8) },
  ];
  const groups = groupHttpHistory(items, now);
  assert.deepEqual(groups.map((group) => [group.day, group.items.map((item) => item.id)]), [
    ["today", ["a", "b"]],
    ["yesterday", ["c"]],
    ["earlier", ["d", "e"]],
  ]);
});

test("filters history by name, URL, method and status", () => {
  const items = [
    { id: "a", method: "GET", url: "https://x/users", name: "用户列表", status: 200, elapsed: 1, createdAt: 1 },
    { id: "b", method: "POST", url: "https://x/orders", status: 500, elapsed: 1, createdAt: 1 },
  ];
  assert.deepEqual(filterHttpHistory(items, "用户", "R").map((item) => item.id), ["a"]);
  assert.deepEqual(filterHttpHistory(items, "post", "R").map((item) => item.id), ["b"]);
  assert.deepEqual(filterHttpHistory(items, "500", "R").map((item) => item.id), ["b"]);
  assert.deepEqual(filterHttpHistory(items, " ", "R").map((item) => item.id), ["a", "b"]);
});
