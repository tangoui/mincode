import assert from "node:assert/strict";
import test from "node:test";

const {
  RECENT_LIMIT,
  avatarColor,
  avatarInitials,
  moveHighlight,
  parseSshAddress,
  pickRecent,
  relativeTime,
} = await import("../src/domain/connections/overview.ts");

test("最近打开：按打开时间倒序取前 8 个，跳过从未打开和清除时间点之前的项", () => {
  assert.equal(RECENT_LIMIT, 8);
  const items = Array.from({ length: 10 }, (_, i) => ({ id: `h${i}`, at: i === 3 ? undefined : 1000 + i }));
  assert.deepEqual(
    pickRecent(items, (x) => x.at, 0).map((x) => x.id),
    ["h9", "h8", "h7", "h6", "h5", "h4", "h2", "h1"],
  );
  assert.deepEqual(pickRecent(items, (x) => x.at, 1007).map((x) => x.id), ["h9", "h8"]);
  assert.deepEqual(pickRecent(items, (x) => x.at, 0, 2).map((x) => x.id), ["h9", "h8"]);
  assert.deepEqual(pickRecent([], () => 1, 0), []);
  // 不修改原数组
  assert.equal(items[0].id, "h0");
});

test("头像颜色按 seed 稳定取自固定调色板", () => {
  const palette = ["#b7791f", "#c05621", "#2f855a", "#0f766e", "#0e7490", "#4f46e5", "#7c3aed", "#be185d"];
  assert.equal(avatarColor("veve"), avatarColor("veve"));
  assert.match(avatarColor("x"), /^#[0-9a-f]{6}$/);
  const seen = new Set();
  for (const seed of ["", "a", "b", "geo线上", "open claw", "支持部", "10.0.0.5", "prod-db", "staging", "veve"]) {
    const color = avatarColor(seed);
    assert.ok(palette.includes(color), `${seed} -> ${color}`);
    seen.add(color);
  }
  assert.ok(seen.size > 1, "不同 seed 应能分到不同颜色");
});

test("头像首字母：拉丁取两个字母，多词取前两词首字母，CJK 取首字，空串为 ?", () => {
  assert.equal(avatarInitials("geo线上"), "GE");
  assert.equal(avatarInitials("open claw"), "OC");
  assert.equal(avatarInitials("支持部"), "支");
  assert.equal(avatarInitials(""), "?");
  assert.equal(avatarInitials("   "), "?");
  assert.equal(avatarInitials("veve"), "VE");
  assert.equal(avatarInitials("  prod  web  01 "), "PW");
  assert.equal(avatarInitials("a"), "A");
  assert.equal(avatarInitials("10.0.0.5"), "10");
  assert.equal(avatarInitials("ログ"), "ロ");
});

test("解析 user@host[:port]", () => {
  assert.deepEqual(parseSshAddress("root@10.0.0.5"), { username: "root", host: "10.0.0.5" });
  assert.deepEqual(parseSshAddress(" www@host.cn:2222 "), { username: "www", host: "host.cn", port: 2222 });
  assert.deepEqual(parseSshAddress("me@[::1]:22"), { username: "me", host: "::1", port: 22 });
  assert.deepEqual(parseSshAddress("me@[fe80::1]"), { username: "me", host: "fe80::1" });
  assert.deepEqual(parseSshAddress("root@host:65535"), { username: "root", host: "host", port: 65535 });
  for (const bad of [
    "root@host:99999",
    "root@host:0",
    "root@host:",
    "root@host:22a",
    "@host",
    "root@",
    "host-only",
    "geo",
    "",
    "a b@host",
    "root@ho st",
    "a@b@host",
    "root@[::1",
    "root@[]",
    "root@[::1]x",
    "root@::1",
  ]) {
    assert.equal(parseSshAddress(bad), null, bad);
  }
});

test("相对时间", () => {
  const now = Date.UTC(2026, 9, 28, 10, 0, 0);
  assert.equal(relativeTime(now - 10 * 60_000, now, "zh-CN"), "10分钟前");
  assert.equal(relativeTime(now - 24 * 3600_000, now, "zh-CN"), "昨天");
  assert.equal(relativeTime(now - 3 * 3600_000, now, "zh-CN"), "3小时前");
  assert.equal(relativeTime(now - 10 * 60_000, now, "en"), "10 minutes ago");
  assert.equal(relativeTime(now - 24 * 3600_000, now, "en"), "yesterday");
  assert.equal(relativeTime(now, now, "en"), "now");
  // 时钟偏差导致的未来时间按“现在”处理
  assert.equal(relativeTime(now + 5_000, now, "en"), "now");
  assert.equal(relativeTime(now - 7 * 86_400_000, now, "en"), "7 days ago");
  // 超过 7 天显示月日；跨年时带上年份
  assert.match(relativeTime(now - 10 * 86_400_000, now, "zh-CN"), /^\d{1,2}月\d{1,2}日$/);
  assert.match(relativeTime(now - 10 * 86_400_000, now, "en"), /^[A-Z][a-z]{2} \d{1,2}$/);
  assert.match(relativeTime(now - 400 * 86_400_000, now, "en"), /2025/);
});

test("键盘高亮移动", () => {
  assert.equal(moveHighlight(-1, 1, 0), -1);
  assert.equal(moveHighlight(3, -1, 0), -1);
  assert.equal(moveHighlight(-1, 1, 3), 0);
  assert.equal(moveHighlight(-1, -1, 3), 0);
  assert.equal(moveHighlight(0, 1, 3), 1);
  assert.equal(moveHighlight(2, 1, 3), 2);
  assert.equal(moveHighlight(0, -1, 3), 0);
  assert.equal(moveHighlight(2, -1, 3), 1);
  // 列表变短后仍夹在范围内
  assert.equal(moveHighlight(5, -1, 3), 2);
});

test("连接地址：host:port、文件路径、库名，都没有时显示托管服务文案", async () => {
  const { connectionEndpoint } = await import("../src/domain/database/endpoint.ts");
  assert.equal(connectionEndpoint({ host: "10.199.1.129", port: 3306 }, "托管服务"), "10.199.1.129:3306");
  assert.equal(connectionEndpoint({ filePath: "/a.db" }, "托管服务"), "/a.db");
  assert.equal(connectionEndpoint({ filePath: "/a.db", host: "h", port: 1 }, "托管服务"), "/a.db");
  assert.equal(connectionEndpoint({ host: "", port: 0, database: "x" }, "托管服务"), "x");
  assert.equal(connectionEndpoint({ database: "x" }, "托管服务"), "x");
  assert.equal(connectionEndpoint({}, "托管服务"), "托管服务");
  assert.equal(connectionEndpoint({ host: "", port: 0, filePath: "", database: "" }, "Managed service"), "Managed service");
});
