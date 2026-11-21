# MinCode Vue 工作区

这是 MinCode 的 Vue 3 主前端工作区。该目录中的代码只通过 **tango 生成的 Go 服务绑定**访问原生能力。

> 开发、评审及自动化修改必须遵守 [Vue App 前端开发规范](docs/FRONTEND_STANDARDS.md)。面向自动化开发的强制入口为 [`AGENTS.md`](AGENTS.md)。

## 技术栈

- Vue 3 + TypeScript + Vite
- Element Plus
- Pinia
- Vue Router（Hash History，适合桌面宿主的资源服务）
- Tailwind CSS v4
- tango Go bindings
- Xterm + Monaco（按业务模块迁移）

## 当前终端能力

- 本地 Shell 通过 Go 后端创建和管理 PTY 会话。
- 支持横向/纵向递归分屏、活动窗格、关闭和重启。
- 分隔条支持鼠标/触控笔拖拽，也可聚焦后使用方向键调整。
- pane tree、活动窗格和分屏比例写入版本化本地快照；损坏或不兼容快照会被丢弃并安全回退到单窗格。
- 可为新 Shell 配置类型、自定义路径、初始 cwd、启动命令和环境变量；为避免泄密或重放副作用，启动命令与环境变量不进入未加密快照。
- 路由切换使用 KeepAlive，并在实际 桌面运行时中短期保留 Xterm 输出快照。
- SSH 终端支持密码、私钥、SSH Agent、keyboard-interactive/MFA、跳板机和连接测试。
- SSH 通道异常关闭后复用 Go 后端的 30 秒 Grace Period 自动恢复；端口转发规则随 SSH 会话启动。
- SSH 资料和布局可以恢复，但密码与私钥口令只保存在当前进程内；应用重启后需要重新输入敏感凭据。

## 当前本地项目能力

- 通过原生目录选择器或项目路径打开工作区，文件树按目录懒加载。
- 支持文件名与文本内容搜索，并可从内容结果跳转到 Monaco 对应行。
- 支持新建文件、新建目录、重命名、删除和在系统文件管理器中显示。
- Monaco 使用每个文件独立模型，支持多标签、撤销历史、未保存标记、保存当前文件和保存全部文件。
- 编辑器字体大小、Tab 宽度、自动换行和缩略图偏好会持久化。
- Git 侧栏显示分支、upstream、ahead/behind 以及 staged、unstaged、untracked 状态。
- 工作区快照不会保存文件内容或未保存内容，只保存项目根目录、打开文件路径和编辑器偏好。

## 本地开发

```bash
# 在仓库根目录安装工作区依赖
pnpm install

# 在本目录执行：浏览器预览（不调用原生服务）
pnpm dev

# 桌面开发：先在本目录启动 Vite（端口可用 VITE_PORT 指定，默认 5150），
# 再在上一级目录让桌面宿主代理到它
pnpm dev
cd .. && TANGO_DEV_URL=http://127.0.0.1:5150 CGO_ENABLED=1 go run -tags nocoredrivers .

# 构建前端：产物写入 ../frontend/dist，由桌面宿主（../main.go）嵌入
pnpm build

# 构建桌面应用（上一级目录）
cd .. && CGO_ENABLED=1 go build -tags production,nocoredrivers -trimpath -ldflags="-w -s" .
```

## 迁移规则

1. 新增原生调用只使用 `src/services/desktop-bridge.ts` 的 `invokeBackend`。
2. 原生能力只通过 `src/services/desktop-bridge.ts` 和 feature service 调用，禁止绕过既有 Go 边界。
3. 先迁移应用壳、共享状态与设计 token，再迁移终端、文件、数据库、Git 和 HTTP 等垂直模块。
4. Monaco、Xterm 和 AG Grid 等专业组件按单独 Vue 封装迁移，不用 Element Plus 替代。
5. 依赖优先升级到最新稳定兼容版；页面运行时（tango 注入的 window.tango）与 Go 侧 tango 模块来自同一版本，不单独升级。

> 2026-07-29 兼容性说明：TypeScript 7.0.2 与当前 `vue-tsc` 3.3.8 不兼容，因此暂时使用已通过类型检查与桌面构建验证的 TypeScript 6.0.3。
