import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

/**
 * AI 能力里写死的 action 名和视图 handler 里的 case 分属两个文件，
 * 类型检查管不到这层配对：改错一边只在模型真的调用时才炸。
 */
const bridges = [
  {
    tools: "database-tools.ts",
    dependency: "databaseAction",
    handler: "src/composables/database/useAiDatabaseActions.ts",
  },
  {
    tools: "redis-tools.ts",
    dependency: "redisAction",
    handler: "src/components/database/RedisWorkspacePanel.vue",
  },
  {
    tools: "http-tools.ts",
    dependency: "httpAction",
    handler: "src/views/HttpWorkspaceView.vue",
  },
];

function readSource(path) {
  return readFile(new URL(`../${path}`, import.meta.url), "utf8");
}

function actionNames(source, dependency) {
  const pattern = new RegExp(
    `${dependency}!\\(\\{[^}]*?action: "([a-z-]+)"`,
    "g",
  );
  return [...new Set([...source.matchAll(pattern)].map((match) => match[1]))];
}

/**
 * 能力是数组里的对象字面量，risk/target 写在 tool 之前。
 * 从工具名往回找起始花括号，才能把整个能力块都圈进来。
 */
function capabilityBlock(source, toolName) {
  const marker = source.indexOf(`name: "${toolName}"`);
  if (marker < 0) return undefined;
  const start = source.lastIndexOf("\n    {", marker);
  const end = source.indexOf("\n    },", marker);
  if (start < 0 || end < 0) return undefined;
  return source.slice(start, end);
}

function toolBlock(source, toolName) {
  const marker = source.search(
    new RegExp(`tool\\(\\s*"${toolName.replaceAll(".", "\\.")}"`),
  );
  if (marker < 0) return undefined;
  const following = source.slice(marker + 1).search(/tool\(\s*"/);
  const next = following < 0 ? -1 : marker + 1 + following;
  return source.slice(marker, next > 0 ? next : source.length);
}

for (const bridge of bridges) {
  test(`every ${bridge.dependency} name has a case in ${bridge.handler}`, async () => {
    const tools = await readSource(`src/services/ai-tools/${bridge.tools}`);
    const handler = await readSource(bridge.handler);
    const actions = actionNames(tools, bridge.dependency);
    assert.ok(
      actions.length,
      `${bridge.tools} must declare at least one ${bridge.dependency} action`,
    );

    const cases = new Set(
      [...handler.matchAll(/case "([a-z-]+)"/g)].map((match) => match[1]),
    );
    const missing = actions.filter((action) => !cases.has(action));
    assert.deepEqual(
      missing,
      [],
      `${bridge.handler} is missing cases for: ${missing.join(", ")}`,
    );
  });
}

test("the table structure drawer capability is wired end to end", async () => {
  const tools = await readSource("src/services/ai-tools/database-tools.ts");
  assert.match(tools, /tool\(\s*"database\.open_table_details"/);
  // 抽屉要靠连接才能取到结构，schema 少一项模型就会漏传。
  const capability = toolBlock(tools, "database.open_table_details");
  assert.ok(
    capability,
    "database.open_table_details capability must be present",
  );
  for (const argument of ["connectionId", "database", "table"]) {
    assert.ok(
      capability.includes(argument),
      `database.open_table_details must require ${argument}`,
    );
  }

  const composable = await readSource(
    "src/composables/database/useAiDatabaseActions.ts",
  );
  // openTableDetails 只弹提示不抛错，没有后置校验时 AI 会把失败当成功。
  assert.match(
    composable,
    /case "open-table-details"[\s\S]*?if \(!options\.detailsVisible\.value\)/,
  );

  const view = await readSource("src/views/DatabaseWorkspaceView.vue");
  assert.match(
    view,
    /openTableDetails: async \(database: string, table: string\)/,
  );
  assert.match(view, /detailsVisible,/);
});

test("SSH server management capabilities cover every ssh_admin command", async () => {
  const tools = await readSource("src/services/ai-tools/ssh-tools.ts");
  const commands = [
    "ssh_admin_snapshot",
    "ssh_admin_logs",
    "ssh_admin_processes",
    "ssh_admin_process_signal",
    "ssh_admin_systemd_list",
    "ssh_admin_systemd_action",
    "ssh_admin_docker_info",
    "ssh_admin_docker_containers",
    "ssh_admin_docker_action",
    "ssh_admin_docker_logs",
  ];
  for (const command of commands) {
    assert.ok(
      tools.includes(`"${command}"`),
      `ssh-tools.ts must invoke ${command}`,
    );
  }

  // 这批后端读平铺参数，误套 payload 会让 sessionId 解不出来，后端只报"未找到会话"。
  assert.match(
    tools,
    /function adminArgs[\s\S]*?return \{ sessionId: metadataString\(resource, "sessionId"\), \.\.\.extra \}/,
  );
  assert.doesNotMatch(tools, /ssh_admin_[a-z_]+", \{ payload:/);

  const listed =
    tools.match(
      /export const sshSessionCapabilityNames = \[([\s\S]*?)\];/,
    )?.[1] ?? "";
  const declared = [
    ...new Set(
      [...tools.matchAll(/name: "(ssh\.[a-z_]+)"/g)].map((match) => match[1]),
    ),
  ];
  const sessionScoped = declared.filter(
    (name) =>
      ![
        "ssh.list_connections",
        "ssh.run_command",
        "ssh.list_known_hosts",
        "ssh.remove_known_host",
        "ssh.discover_config",
      ].includes(name),
  );
  const missing = sessionScoped.filter((name) => !listed.includes(`"${name}"`));
  assert.deepEqual(
    missing,
    [],
    `sshSessionCapabilityNames is missing: ${missing.join(", ")}`,
  );
});

test("mutating SSH server management capabilities stay behind approval", async () => {
  const tools = await readSource("src/services/ai-tools/ssh-tools.ts");
  for (const name of [
    "ssh.signal_process",
    "ssh.service_action",
    "ssh.container_action",
    "ssh.remove_known_host",
  ]) {
    const capability = capabilityBlock(tools, name);
    assert.ok(capability, `${name} capability must be present`);
    assert.ok(
      capability.includes('risk: "dangerous"'),
      `${name} must be dangerous`,
    );
    assert.ok(
      capability.includes("approvalPreview"),
      `${name} must show an approval preview`,
    );
  }
});

test("SSH security capabilities are not bound to a live session", async () => {
  const tools = await readSource("src/services/ai-tools/ssh-tools.ts");
  for (const name of [
    "ssh.list_known_hosts",
    "ssh.remove_known_host",
    "ssh.discover_config",
  ]) {
    const capability = capabilityBlock(tools, name);
    assert.ok(capability, `${name} capability must be present`);
    // known_hosts 与 ~/.ssh/config 是本机文件，套 schema() 会逼模型填不存在的 SSH 资源。
    assert.ok(
      capability.includes('target: "none"'),
      `${name} must not require an SSH resource`,
    );
    assert.ok(
      !capability.includes("inputSchema: schema("),
      `${name} must not use the resource-target schema`,
    );
  }
});

test("schema sync capabilities reach the database view and gate their change ids", async () => {
  const tools = await readSource("src/services/ai-tools/database-tools.ts");
  const actions = actionNames(tools, "databaseAction");
  for (const action of [
    "compare-schema",
    "generate-schema-sync",
    "execute-schema-sync",
  ]) {
    assert.ok(
      actions.includes(action),
      `database-tools.ts must route ${action}`,
    );
  }
  const execute = toolBlock(tools, "database.execute_schema_sync");
  assert.ok(execute, "database.execute_schema_sync capability must be present");
  assert.ok(
    execute.includes('risk: "dangerous"'),
    "executing a schema sync must be dangerous",
  );
  assert.ok(
    execute.includes("approvalPreview"),
    "executing a schema sync must show an approval preview",
  );

  const composable = await readSource(
    "src/composables/database/useAiDatabaseActions.ts",
  );
  // 指纹必须来自刚做的对比，拿旧值后端会直接拒绝执行。
  assert.match(
    composable,
    /case "execute-schema-sync"[\s\S]*?comparison\.source\.fingerprint/,
  );
  // 改动 id 不校验的话，模型编一个不存在的 id 会静默同步成默认全集。
  assert.match(
    composable,
    /function schemaSyncChangeIds[\s\S]*?received unknown change ids/,
  );
  assert.match(
    composable,
    /connectedSchemaSyncSelection[\s\S]*?requires two different connections/,
  );
});
