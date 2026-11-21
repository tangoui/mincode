# MinCode（tango 桌面版）

MinCode 的桌面应用：Go 后端 + Vue 前端，由 [tango](../tango) 的系统 WebView 承载。Go 代码在根目录，一个模块（`mincode/app`）、一个 `go.mod`；前端源码在 `vue-app/`。

## 目录

| 目录 | 内容 |
|---|---|
| `main.go` | 桌面入口：窗口、托盘、菜单、单实例、后端启动 |
| `internal/host/` | 宿主胶水层：tango ipc 处理器（命令调用、原生能力、页面事件）、后端流适配、事件按序下发与大事件引用 |
| `backend/` | 按业务域组织的 Go 命令与服务；不依赖 tango，经 `core.SetEmitter` / `core.SetStreamRegistrar` 与宿主对接 |
| `plugins/api/` | 插件开发约定（贡献点 schema、清单格式） |
| `official-plugins/` | 官方插件源码；插件由现有 `backend/plugin/` 管理 |
| `packages/` | Node / Go 插件 SDK、Go 协议测试与共享插件 UI |
| `tools/mincode-cli/`、`tools/plugin-devkit/`、`tools/plugin-templates/` | `mincode plugin` 命令、开发工具和模板 |
| `mobile/` | Flutter 移动伴侣：Android 手机/平板、iPhone/iPad，通过现有 MinCode Cloud 配对 |
| `tools/` | 发布辅助工具（如更新清单签名） |
| `vue-app/` | Vue 前端源码；经 `src/services/desktop-*.ts` 使用 tango 注入的运行时 `window.tango` |
| `frontend/dist/` | 前端构建产物（`vue-app` 的 `pnpm build` 写入），由 `main.go` 以 `//go:embed` 嵌入 |
| `icons/` | 应用、托盘、安装包图标与 DMG 背景 |
| `smoketest/` | 真实窗口冒烟：调用、取消、原生能力、流、事件顺序与引用、页面广播 |
| `e2e/` | 整机自动化测试（tango automation，窗口不抢前台） |
| `tan.json` | tango 工具链配置：`tan dev` / `tan build` / `tan package` |

## 开发与构建

在本目录执行：

```sh
# 安装本仓库的依赖，构建插件 CLI 并启动 Tango 桌面端
pnpm install --frozen-lockfile
pnpm dev

# 构建前端（产物写入 frontend/dist）
(cd vue-app && pnpm build)

# 构建桌面应用（仅应用主程序）
CGO_ENABLED=1 go build -tags production,nocoredrivers -trimpath -ldflags="-w -s" .

# 使用同级 Tango 源码打包桌面应用和插件 CLI，输出到 dist/desktop/
pnpm package:desktop

# 也可以用从同级 Tango 源码安装的 tan 直接打包
tan package -o dist/desktop

# 测试
CGO_ENABLED=1 go test ./internal/... ./backend/...
CGO_ENABLED=1 TANGO_LAUNCH_BACKGROUND=1 go run ./smoketest
CGO_ENABLED=1 TANGO_E2E=1 go test ./e2e -run E2E -v
```

两个打包命令都读取 `tan.json` 的 `desktopBuildTags`，默认使用正式版配置，不内置 DuckDB、Oracle、YashanDB 和 ClickHouse 驱动；这些数据库仍可通过对应的驱动插件连接。无需手动设置 `GOFLAGS`。旧版 `tan` 不识别该字段，可从本目录运行 `go -C ../tango/cmd/tan install .` 更新。

E2E 每次用独立身份启动应用（`MINCODE_APP_NAME` / `MINCODE_APP_ID` + 临时 `MINCODE_DATA_DIR`）：tango 按应用名给 WebView 本地存储分目录（`~/Library/WebKit/<应用名>`），按 ID 做单实例，所以测试既不带入也不改写平时运行的界面状态，测完删掉自己的存储目录。

## 插件与移动端

- 自写插件：桌面安装包自带 `mincode` CLI，可在应用内安装到命令行；源码开发可用 `pnpm build:cli` 单独构建。`plugin dev` 默认连接 Tango 的数据目录，可用 `MINCODE_DATA_DIR` 或 `MINCODE_PLUGIN_DEV_DISCOVERY` 显式覆盖。参见[插件开发指南](docs/development/plugin-development.md)。
- 官方插件：用 `pnpm verify:plugins` 检查共享契约、运行时和共享 UI 副本，用 `pnpm --dir vue-app build:plugin-packages <插件名>` 构建本机安装包。Draw 需先运行 `pnpm prepare:draw-math`。插件发布与上传是独立命令，见[打包与发布工具](tools/release/README.md)。
- 移动端：在 `mobile/` 内用 `flutter pub get`、`flutter test` 和 `flutter run`。包名、配对协议、更新接口保持原样；正式签名和发布参见 [移动端发布](docs/operations/mobile-release.md)。签名与本机配置不随源码迁移。
