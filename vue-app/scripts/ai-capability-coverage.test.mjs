import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

/**
 * 这批能力是直接对接后端命令的，参数名、审批档位、路径防护都写在源码字符串里，
 * 类型检查看不出对错：字段拼错只在模型真的调用时才炸，档位写松了没人会报错。
 */
function readTools(name) {
  return readFile(
    new URL(`../src/services/ai-tools/${name}`, import.meta.url),
    "utf8",
  );
}

/**
 * 能力块在各文件里的缩进和收尾括号写法都不一样，按括号匹配不可靠。
 * 锚点用 `tool("名字"` 而不是裸名字：裸名字会先命中文件顶部的名单数组。
 */
function capabilityBlock(source, toolName) {
  const marker = source.search(new RegExp(`tool\\(\\s*"${toolName}"`));
  if (marker < 0) return undefined;
  const following = source.slice(marker + 1).search(/tool\(\s*"/);
  const next = following < 0 ? -1 : marker + 1 + following;
  return source.slice(marker, next > 0 ? next : source.length);
}

test("workspace capability contracts remain registerable", async () => {
  const tools = await readTools("workspace-tools.ts");
  const refresh = tools.match(
    /"workspace\.refresh_file_index",[\s\S]*?mode: "(read|write)" as const,[\s\S]*?risk: "(read|network|write|dangerous)" as const,/,
  );
  assert.ok(refresh, "workspace.refresh_file_index capability must be present");
  assert.equal(
    refresh[1],
    "read",
    "metadata-only index refresh must use read mode",
  );
  assert.equal(
    refresh[2],
    "read",
    "metadata-only index refresh must use read risk",
  );
});

test("language server capabilities keep the model inside the bound project", async () => {
  const tools = await readTools("workspace-tools.ts");
  for (const command of [
    "project_lsp_list_servers",
    "project_lsp_start",
    "project_lsp_stop",
    "project_lsp_request",
    "project_lsp_notify",
    "project_lsp_logs",
    "project_lsp_install",
  ]) {
    assert.ok(
      tools.includes(`"${command}"`),
      `workspace-tools.ts must invoke ${command}`,
    );
  }

  // 后端只按 root 找会话，不校验 params 里的文档 uri：不挡就能让语言服务器代读项目外的文件。
  assert.match(
    tools,
    /function assertLspUriIsSafe[\s\S]*?must stay inside the bound project/,
  );
  assert.match(
    tools,
    /params:\s*lspParams\(\s*input,\s*resourceMetadataPath\(resource, "projectRoot"\),\s*\)/,
  );

  // request 能发任意 LSP 方法，包含 workspace/executeCommand 这种会让服务器代跑命令的。
  assert.match(tools, /const LSP_QUERY_METHODS = \[([\s\S]*?)\] as const/);
  const queryMethods = tools.match(
    /const LSP_QUERY_METHODS = \[([\s\S]*?)\] as const/,
  )[1];
  assert.ok(
    !queryMethods.includes("executeCommand"),
    "the query allowlist must not expose workspace/executeCommand",
  );
  assert.match(
    tools,
    /lspMethod\(\s*input,\s*LSP_QUERY_METHODS,\s*"workspace\.language_server_query",?\s*\)/,
  );

  // 装语言服务器会在本机跑包管理器，必须走审批。
  const install = capabilityBlock(tools, "workspace.install_language_server");
  assert.ok(
    install,
    "workspace.install_language_server capability must be present",
  );
  assert.ok(
    install.includes('risk: "dangerous"'),
    "installing a language server must be dangerous",
  );
  assert.ok(
    install.includes("approvalPreview"),
    "installing a language server must show an approval preview",
  );

  const listed =
    tools.match(
      /export const workspaceCapabilityNames = \[([\s\S]*?)\];/,
    )?.[1] ?? "";
  for (const name of [
    "workspace.list_language_servers",
    "workspace.start_language_server",
    "workspace.stop_language_server",
    "workspace.language_server_logs",
    "workspace.install_language_server",
    "workspace.language_server_query",
    "workspace.language_server_notify",
  ]) {
    assert.ok(
      listed.includes(`"${name}"`),
      `workspaceCapabilityNames must list ${name}`,
    );
  }
});

test("MCP capabilities cover the server contract and gate arbitrary tool calls", async () => {
  const tools = await readTools("settings-tools.ts");
  for (const command of [
    "mcp_list_status",
    "mcp_list_tools",
    "mcp_list_resources",
    "mcp_read_resource",
    "mcp_list_prompts",
    "mcp_get_prompt",
  ]) {
    assert.ok(
      tools.includes(`"${command}"`),
      `settings-tools.ts must invoke ${command}`,
    );
  }
  // mcp_call_tool goes through the cancellable helper (services/mcp-tool-call.ts).
  assert.ok(tools.includes("callMcpToolCancellable("), "settings-tools.ts must call MCP tools");

  // 连接能力只能让模型选已保存配置的 ID，原始命令、URL、header 和环境变量留在可信闭包里。
  const connect = capabilityBlock(tools, "settings.connect_saved_mcp_server");
  assert.ok(
    connect,
    "settings.connect_saved_mcp_server capability must be present",
  );
  assert.ok(
    connect.includes('risk: "dangerous"'),
    "connecting a saved MCP server must be dangerous",
  );
  assert.ok(
    connect.includes("connectSavedMcpServer"),
    "the capability must delegate to the saved-config resolver",
  );
  for (const field of [
    "command:",
    "args:",
    "env:",
    "url:",
    "headers:",
    "transport:",
  ]) {
    assert.ok(
      !connect.includes(field),
      `the model-facing MCP connection capability must not expose ${field}`,
    );
  }

  // MCP 工具是外部服务器定义的，这里预知不了它会做什么，只能一律走审批。
  const callTool = capabilityBlock(tools, "settings.call_mcp_tool");
  assert.ok(callTool, "settings.call_mcp_tool capability must be present");
  assert.ok(
    callTool.includes('risk: "dangerous"'),
    "calling an external MCP tool must be dangerous",
  );
  assert.ok(
    callTool.includes("approvalPreview"),
    "calling an external MCP tool must show an approval preview",
  );
});

test("mock server capabilities translate the delay field the backend actually reads", async () => {
  const tools = await readTools("http-tools.ts");
  for (const command of [
    "mock_server_list",
    "mock_server_start",
    "mock_server_stop",
  ]) {
    assert.ok(
      tools.includes(`"${command}"`),
      `http-tools.ts must invoke ${command}`,
    );
  }
  // 后端 mockRule 的字段是 delay_ms，直接透传 delayMs 会被静默丢成 0。
  assert.match(
    tools,
    /function mockRules[\s\S]*?delay_ms: typeof rule\.delayMs === "number"/,
  );
  const start = capabilityBlock(tools, "http.start_mock_server");
  assert.ok(start, "http.start_mock_server capability must be present");
  assert.ok(
    start.includes("approvalPreview"),
    "starting a mock server must show an approval preview",
  );
});

test("database schema inspection can connect a saved target without bypassing approval", async () => {
  const tools = await readTools("database-tools.ts");
  const describe = capabilityBlock(tools, "database.describe_schema");
  assert.ok(describe, "database.describe_schema capability must be present");
  assert.ok(
    tools.match(
      /databaseSavedCapabilityNames = \[[\s\S]*?"database\.describe_schema"/,
    ),
    "saved database connections must advertise schema inspection",
  );
  assert.ok(
    describe.includes('risk: "network"'),
    "opening a database session must require network approval",
  );
  assert.ok(
    describe.includes("dependencies.connectDatabase(connectionId)"),
    "schema inspection must connect the selected saved database when needed",
  );
  assert.ok(
    describe.includes("dependencies.databaseSessionId?.(connectionId)"),
    "schema inspection must resolve the session created by the connection step",
  );
  assert.ok(
    describe.includes('dependencies.invoke("db_get_schema", { sessionId })'),
    "schema inspection must read through the established session",
  );
});

test("database write capabilities stay behind approval and reach the right session", async () => {
  const tools = await readTools("database-tools.ts");
  for (const command of [
    "db_cancel_query",
    "db_vector_create_collection",
    "db_check_odbc_drivers",
  ]) {
    assert.ok(
      tools.includes(`"${command}"`),
      `database-tools.ts must invoke ${command}`,
    );
  }

  for (const name of [
    "database.import_rows",
    "database.create_vector_collection",
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
    assert.ok(
      capability.includes('metadataString(resource, "sessionId")'),
      `${name} must use the bound session`,
    );
  }

  // 导入的 data 可能是整表内容，审批文案带上就等于把数据写进日志。
  const importRows = capabilityBlock(tools, "database.import_rows");
  assert.ok(
    importRows.includes("data redacted"),
    "database.import_rows must redact the payload in its preview",
  );

  const listed =
    tools.match(
      /export const databaseSessionCapabilityNames = \[([\s\S]*?)\];/,
    )?.[1] ?? "";
  for (const name of [
    "database.import_rows",
    "database.cancel_query",
    "database.create_vector_collection",
  ]) {
    assert.ok(
      listed.includes(`"${name}"`),
      `databaseSessionCapabilityNames must list ${name}`,
    );
  }
});

test("system capabilities treat the clipboard and screen as sensitive", async () => {
  const tools = await readTools("system-tools.ts");
  for (const command of [
    "detect_cli_tools",
    "check_update",
    "clipboard_read_text",
    "clipboard_write_text",
    "capture_screenshot",
  ]) {
    assert.ok(
      tools.includes(`"${command}"`),
      `system-tools.ts must invoke ${command}`,
    );
  }

  // 剪贴板里常年躺着刚复制的密码，读一次就等于交给模型；截图同理。
  for (const name of ["system.read_clipboard", "system.capture_screenshot"]) {
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

  // 主目录绝对路径是越出项目边界的起点，不给模型。
  assert.ok(
    !tools.includes("get_user_home_dir"),
    "system-tools.ts must not expose the absolute home directory",
  );
});

test("browser capabilities keep page control on the AI-gated path", async () => {
  const tools = await readTools("browser-tools.ts");
  // navigate/reload/get_url/eval 走 embedded_browser_ai_execute，那条路才检查共享开关。
  for (const command of [
    "embedded_browser_navigate",
    "embedded_browser_reload",
    "embedded_browser_get_url",
    "embedded_browser_eval",
  ]) {
    assert.ok(
      !tools.includes(`"${command}"`),
      `browser-tools.ts must not bypass the AI gate with ${command}`,
    );
  }
  assert.match(tools, /invoke\("embedded_browser_ai_execute"/);

  // 清数据是整个 WebView 范围的，会把所有站点登出。
  const clear = capabilityBlock(tools, "browser.clear_browsing_data");
  assert.ok(clear, "browser.clear_browsing_data capability must be present");
  assert.ok(
    clear.includes('risk: "dangerous"'),
    "clearing browsing data must be dangerous",
  );

  const listed =
    tools.match(/export const browserCapabilityNames = \[([\s\S]*?)\];/)?.[1] ??
    "";
  for (const name of [
    "browser.set_zoom",
    "browser.close_tab",
    "browser.clear_browsing_data",
  ]) {
    assert.ok(
      listed.includes(`"${name}"`),
      `browserCapabilityNames must list ${name}`,
    );
  }
});

test("SFTP and Git additions avoid local paths and sensitive contents", async () => {
  const sftp = await readTools("sftp-tools.ts");
  for (const command of [
    "sftp_get_home",
    "sftp_transfer_list",
    "sftp_transfer_cancel",
  ]) {
    assert.ok(
      sftp.includes(`"${command}"`),
      `sftp-tools.ts must invoke ${command}`,
    );
  }
  // 这三个后端命令收的是本机绝对路径，暴露出去等于绕开 workspace 的根目录约束。
  for (const command of [
    "sftp_upload_file",
    "sftp_download",
    "sftp_transfer_start",
  ]) {
    assert.ok(
      !sftp.includes(`"${command}"`),
      `sftp-tools.ts must not expose local-path command ${command}`,
    );
  }

  const transfer = capabilityBlock(sftp, "sftp.workspace_transfer");
  assert.ok(transfer, "sftp.workspace_transfer capability must be present");
  assert.ok(
    transfer.includes('risk: "dangerous"'),
    "workspace file transfer must be dangerous",
  );
  assert.ok(
    transfer.includes('"sftp_workspace_transfer_start"'),
    "workspace file transfer must use the bounded backend command",
  );
  assert.ok(
    transfer.includes("safeWorkspacePath"),
    "workspace file transfer must validate a project-relative path",
  );
  assert.ok(
    transfer.includes("safeRemotePath"),
    "workspace file transfer must reject sensitive remote paths",
  );
  assert.ok(
    !transfer.includes("localPath"),
    "workspace file transfer must not accept a local absolute path",
  );

  const git = await readTools("git-tools.ts");
  // 整包暂存 diff 里可能夹着 .env，逐文件的 git.diff 挡得住、这里挡不住就白挡了。
  assert.match(
    git,
    /function redactSensitivePatch[\s\S]*?sensitive file section\(s\) redacted/,
  );
  assert.match(
    git,
    /redactSensitivePatch\(await invoke\("git_get_staged_patch"/,
  );
  const listed =
    git.match(/export const gitCapabilityNames = \[([\s\S]*?)\];/)?.[1] ?? "";
  for (const name of [
    "git.staged_patch",
    "git.read_file_at_ref",
    "git.reset",
    "git.revert_hunk",
  ]) {
    assert.ok(
      listed.includes(`"${name}"`),
      `gitCapabilityNames must list ${name}`,
    );
  }
});

test("database import executes through shared boundary on the approved bound session", async () => {
  const { databaseCapabilities } = await import("../src/services/ai-tools/database-tools.ts");
  const calls = [];
  // The SQL safety net is covered by sql-safety-ai-tools.test.mjs; here it only hands out a token.
  const sqlSafety = {
    runWithSafety: async (options) => options.execute({ checkId: "chk-import" }),
    sqlSafetyArgs: (token) => ({ checkId: token.checkId }),
    confirm: async () => undefined,
  };
  const capabilities = databaseCapabilities({ invoke: async (command, args) => { calls.push({ command, args }); return 2; }, sqlSafety });
  const capability = capabilities.find(item => item.tool.name === "database.import_rows");
  assert.equal(capability.risk, "dangerous");
  const input = { database: "reviewed", table: "items", format: "json", data: '[{"id":1}]' };
  const context = { input, resource: { id: "database:conn", metadata: { connectionId: "conn", sessionId: "bound-session" } } };
  const preview = await capability.approvalPreview(context);
  assert.ok(!preview.includes(input.data));
  assert.equal(await capability.execute(context), 2);
  assert.deepEqual(calls, [{ command: "db_import_data", args: { sessionId: "bound-session", ...input, checkId: "chk-import" } }]);
});
