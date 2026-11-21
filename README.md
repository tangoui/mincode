# MinCode

一个用 [Tango](https://github.com/tangoui/tango) 写的桌面开发工作台：终端、SSH/SFTP、数据库、HTTP 调试、笔记、AI 助手和 AI CLI 会话放在同一个窗口里。

这个仓库是 MinCode 的开源精简版，也是一个"中等规模的 Tango 应用长什么样"的完整例子：Go 后端加 Vue 前端，由 Tango 的系统 WebView 承载，用 `tan` 打包，并通过 GitHub Releases 自动更新。

[English](README.en.md)

## 功能

| 模块 | 说明 |
|---|---|
| 仪表盘 | 常用连接、最近会话和快捷入口 |
| 终端 | 本地终端，多标签、分屏，默认从家目录启动，可选择文件夹 |
| SSH / SFTP | 主机管理、密钥与跳板机、远程终端和文件传输 |
| 数据库 | MySQL、PostgreSQL、SQLite、SQL Server、Redis、MongoDB、Elasticsearch、Cassandra、Neo4j、InfluxDB、TDengine、Trino 等；SQL 编辑器、结果表格、表结构对比、导入导出、备份 |
| HTTP | 请求调试、集合、环境变量、cURL 导入、代码生成、WebSocket / SSE / Socket.IO |
| 笔记 | Markdown 笔记 |
| CC 会话 | 浏览 Claude Code、Codex 等 AI CLI 的本地会话记录 |
| AI 助手 | 接入自己的模型（OpenAI 兼容、Anthropic 等）或本地 Agent（ACP），可调用终端、数据库、HTTP 等工具 |
| 设置 | 外观、快捷键、终端、AI、数据导入导出、检查更新 |

本仓库不包含商业版的项目编辑器、Git、调试器、内置浏览器、插件系统和云端服务。DuckDB、Oracle、YashanDB、ClickHouse 驱动未内置。

## 系统要求

- macOS 12+、Windows 10+（x64）、Linux（x64）
- 从源码构建：Go（版本见 `go.mod`）、Node.js 26、pnpm 11（见 `package.json` 的 `packageManager`）、C 编译器（需启用 cgo）

## 开发

```sh
pnpm install --frozen-lockfile
go install github.com/tangoui/tango/cmd/tan@latest   # 版本尽量与 go.mod 中的 tango 一致
pnpm dev                                             # tan dev：启动桌面端并热更新前端
```

## 构建与测试

```sh
(cd vue-app && pnpm build)                           # 前端产物写入 frontend/dist，由 main.go 嵌入
CGO_ENABLED=1 go build -tags production,nocoredrivers .
pnpm package:desktop                                 # tan package，安装包输出到 dist/

CGO_ENABLED=1 go test ./internal/... ./backend/...
(cd vue-app && pnpm lint && pnpm typecheck && pnpm test && pnpm test:components)
CGO_ENABLED=1 TANGO_LAUNCH_BACKGROUND=1 go run ./smoketest
CGO_ENABLED=1 TANGO_E2E=1 go test ./e2e -run E2E -v
```

E2E 测试每次用独立的应用名、ID 和临时数据目录启动（`MINCODE_APP_NAME`、`MINCODE_APP_ID`、`MINCODE_DATA_DIR`），不会读写你平时使用的数据。

## 发布与自动更新

推送 `vX.Y.Z` 标签后，GitHub Actions 会在四个平台上打包，生成签名的更新清单和增量补丁，并创建一个 Release 草稿；草稿发布后，已安装的客户端才会收到更新。更新由 Tango 的 `service/updater` 完成：先校验清单签名和文件摘要，优先下载补丁，失败时回退完整包，新版本启动后确认，确认失败则回滚。

首次发布前需要用 `tan keygen` 生成密钥，把私钥存入仓库 Secret `TANGO_UPDATE_KEY`。完整步骤见 [docs/release.md](docs/release.md)。

安装包默认未做代码签名。macOS 首次打开时如被拦截，可在"系统设置 → 隐私与安全性"中选择"仍要打开"，或执行 `xattr -dr com.apple.quarantine /Applications/MinCode.app`。

## 目录

| 目录 | 内容 |
|---|---|
| `main.go` | 桌面入口：窗口、托盘、菜单、单实例、更新确认、后端启动 |
| `internal/host/` | 宿主胶水层：Tango IPC 处理器、后端流适配、事件按序下发 |
| `backend/` | 按业务域组织的 Go 命令与服务，不依赖 Tango |
| `vue-app/` | Vue 3 + TypeScript 前端 |
| `frontend/dist/` | 前端构建产物（不入库），由 `main.go` 以 `//go:embed` 嵌入 |
| `smoketest/`、`e2e/` | 真实窗口冒烟与整机自动化测试（窗口不抢前台） |
| `tools/e2e-sshd/` | E2E 用的本地 SSH 服务 |
| `.github/workflows/` | CI、打包发布、更新清单定期重签 |
| `tan.json` | Tango 工具链配置 |

## 许可证

[AGPL-3.0](LICENSE)。
