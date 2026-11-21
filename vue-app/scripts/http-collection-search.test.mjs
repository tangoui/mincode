// 请求集合搜索的纯过滤逻辑（domain/http/request-collection.ts）：
// 命中请求名/URL/方法时保留祖先链路并给出自动展开集合；
// 集合名命中保留整棵子树；空输入原样返回。
import assert from "node:assert/strict";
import test from "node:test";
import { filterHttpCollections } from "../src/domain/http/request-collection.ts";

function request(id, name, method = "GET", url = "") {
  return {
    id,
    name,
    method,
    url,
    params: [],
    headers: [],
    body: "",
    authType: "none",
    documentation: "",
    updatedAt: 0,
  };
}

const tree = [
  {
    id: "c1",
    name: "测试",
    requests: [request("r1", "新建请求11"), request("r2", "登录", "POST", "/api/login")],
    children: [
      { id: "c1-1", name: "子集合", requests: [request("r3", "用户查询", "GET", "/api/users")] },
    ],
  },
  { id: "c2", name: "订单", requests: [request("r4", "创建订单", "POST", "/api/orders")] },
];

test("empty query returns the original list and no expansion", () => {
  const result = filterHttpCollections(tree, "  ");
  assert.equal(result.collections.length, 2);
  assert.equal(result.expandedIds.size, 0);
});

test("matches request name and keeps the ancestor chain expanded", () => {
  const result = filterHttpCollections(tree, "用户查询");
  assert.equal(result.collections.length, 1);
  assert.equal(result.collections[0].id, "c1");
  assert.equal(result.collections[0].children?.length, 1);
  assert.equal(result.collections[0].children?.[0].requests.length, 1);
  assert.deepEqual([...result.expandedIds].sort(), ["c1", "c1-1"]);
});

test("top-level direct request match expands its own collection", () => {
  // 之前漏掉的场景：直属请求命中时集合自身不在 expandedIds，
  // 命中项藏在折叠态里，看起来像搜不到。
  const result = filterHttpCollections(tree, "新建请求11");
  assert.equal(result.collections.length, 1);
  assert.equal(result.collections[0].requests.length, 1);
  assert.ok(result.expandedIds.has("c1"), "matched collection must be expanded");
});

test("collection name match expands the collection itself", () => {
  const result = filterHttpCollections(tree, "测试");
  assert.ok(result.expandedIds.has("c1"), "name-matched collection must be expanded");
});

test("matches url and method case-insensitively", () => {
  const byUrl = filterHttpCollections(tree, "/API/LOGIN");
  assert.equal(byUrl.collections[0].requests.length, 1);
  const byMethod = filterHttpCollections(tree, "post");
  assert.equal(byMethod.collections.length, 2);
});

test("collection name hit keeps the whole subtree", () => {
  const result = filterHttpCollections(tree, "订单");
  assert.equal(result.collections.length, 1);
  assert.equal(result.collections[0].requests.length, 1);
});

test("no match yields an empty list", () => {
  const result = filterHttpCollections(tree, "不存在的接口");
  assert.equal(result.collections.length, 0);
});
