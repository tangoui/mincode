# Vue App 开发约束

本文件适用于 `vue-app/` 下的一切新增与修改。必须同时遵守仓库根目录的 [`../AGENTS.md`](../AGENTS.md)、其路由的前端任务 Skill [`../.agents/skills/mincode-vue-frontend/SKILL.md`](../.agents/skills/mincode-vue-frontend/SKILL.md) 和本文；出现冲突时，采用**更具体、更严格**的规则。详细的评审与验收标准见 [`docs/FRONTEND_STANDARDS.md`](docs/FRONTEND_STANDARDS.md)。

`vue-app/` 是 MinCode 的**唯一前端实现**：Vue 3 + TypeScript + Vite + Pinia + Element Plus，运行在 tango + Go 桌面宿主中。原生能力只通过 Go 边界实现。

## 1. 强制边界

1. **Vue 写法：** 组件使用 `<script setup lang="ts">`；保持 `strict`、`noUnusedLocals`、`noUnusedParameters` 通过。禁止新增 `any`、`@ts-ignore`、`@ts-nocheck` 或无说明的 `eslint-disable`。
2. **原生调用：** tango Go 是唯一原生边界。组件、view、composable 不得直接动态导入 bindings；统一通过 `src/services/desktop-bridge.ts` 和 feature service 调用 `invokeBackend`、`listenAppEvent`、`emitAppEvent`。
3. **运行时差异：** 调用桌面能力前必须处理 `isDesktopRuntime()`。浏览器预览可提供明确标识的演示/降级状态，但不能把假数据或失败伪装成真实成功。
4. **i18n：** 宿主用户可见文本（含 placeholder、tooltip、aria-label、toast、错误与确认文案）必须走 `src/i18n/messages.ts`；同一变更同步维护 `src/i18n/locales/zh.ts` 和 `src/i18n/locales/en.ts`。隔离的插件 UI 使用随插件打包的中英文字典；`src/plugin-ui/` 的共享文案放在本目录，插件专属文案放在插件源码内，禁止导入宿主语言注册表。插件菜单和命令标题使用清单的 `titleLocalized`，不得向宿主语言文件添加插件专属 key。
5. **生成物：** 不修改 `src/generated/`、`../frontend/dist/`（桌面宿主嵌入的构建产物）或 `components.d.ts` 中的生成内容。
6. **安全：** 禁止把密码、Token、私钥、连接串和敏感结果写入源码、日志、普通 localStorage、提示文案或截图。数据库、终端、HTTP、文件和 AI 返回内容一律按不可信输入处理。

## 2. 目录职责与数据流

```text
src/
├── components/       # 可复用业务组件；ui/ 放跨模块基础控件，layout/ 放应用骨架
├── composables/      # 以 use* 命名的可复用响应式行为与生命周期封装
├── domain/           # 纯类型、转换、校验、过滤、补全等无 UI/IPC 依赖逻辑
├── i18n/             # messages.ts 与 zh/en locale
├── router/           # 路由定义与路由级懒加载
├── services/         # 桌面桥（desktop-bridge / desktop-runtime）、持久化及其他外部副作用边界
├── stores/           # Pinia 跨组件/持久化领域状态与异步 action
├── styles/           # foundation、shared、workspaces/<feature> 的样式归属
└── views/            # 路由/工作区级编排，不堆叠底层实现细节
```

- **View** 负责页面编排、路由接入和 feature 入口；复杂工具栏、表格、dialog、编辑器下沉为组件。
- **Component** 通过明确类型的 props/emits 通信；不修改 props，不把无关 store 当作隐式输入。
- **Composable** 管理可复用订阅、交互和资源生命周期，必要时暴露 `dispose`/停止方法。
- **Store** 只保存跨组件、跨页面或可持久化的领域状态；hover、单输入框焦点、单个 dialog 开关等短生命周期状态留在组件。
- **Service** 集中管理 IPC、序列化、持久化和浏览器/桌面差异；避免同一种 IPC 调用散落在多个组件中。
- **Domain** 必须保持可单测、确定性和无副作用；不得依赖 Vue、DOM、Element Plus 或桌面运行时。

对新能力采用以下顺序：`domain type/helper → service/native contract → Pinia store（如需要共享状态）→ component/view → i18n/test`。异步操作必须处理 loading、empty、error、cancel 与过期响应，且在卸载时清理事件、计时器、observer、终端/流式资源。

## 3. 组件与样式复用

开发前先搜索并复用现有实现，尤其是：

- `src/components/ui/AppSearchField.vue`：标准查询输入框；
- `src/components/ui/AppFilterButton.vue`：紧凑筛选按钮；
- `src/components/layout/`：工作台骨架、tab、sidebar、窗口控件；
- `src/composables/`、`src/domain/`：已有筛选、搜索、格式化和生命周期逻辑；
- `src/styles/shared/`：跨 workspace 的交互、控件、菜单、密度与主题样式。

**抽取规则：** 同一交互/视觉模式在两个模块中出现时评估是否可配置复用；达到三个模块时默认抽取为公共组件、composable 或 shared style，除非语义和生命周期显著不同。公共组件 API 必须保持小而稳定，使用 slots/props 表达差异，而不是复制一份近似组件。

### UI 视觉规则

- Element Plus 是唯一的基础业务组件库；Tailwind CSS v4 仅用于布局和局部组合。不得引入第二套 UI 框架。
- 使用 `--app-*` 和 `--el-*` 语义 token，不得为局部页面硬编码近似主题色。新语义色先定义 token，再在组件中使用。
- 样式按归属放置：全局基础在 `styles/foundation/`，跨页面规则在 `styles/shared/`，功能样式在 `styles/workspaces/<feature>/`。`src/styles.css` 的 import 顺序是级联契约，禁止排序或随意移动。
- 新 class 使用清晰的 feature 前缀和 BEM-like 命名（`block__element`、`is-*`）；避免无作用域标签选择器、全局 `.el-*` 覆盖、无理由 `!important` 和魔法 `z-index`。
- 不使用装饰性阴影建立层级；使用背景、细边框、间距和选中态。

### 主题变量与 Design Token

- `--app-*` 是基于标准 CSS Custom Properties 的 MinCode 语义化设计令牌，不属于 Tailwind，也不得与 Element Plus 管理的 `--el-*` 变量混为一层。
- 全局 token 按语义角色命名，例如 `--app-canvas`、`--app-surface`、`--app-border`、`--app-text`、`--app-accent`；共享组件 token 使用 `--app-{component}-{property}[-{state}]`，例如 `--app-input-border-focus`；公共尺寸按用途命名，例如 `--app-search-field-height`。禁止使用 `--app-blue-500`、`--app-white` 这类绑定具体颜色的名称。
- 全局 token 与主题覆盖统一定义在 `styles/foundation/foundation.css`；跨 workspace 的组件 token 放在对应 `styles/shared/` 文件；功能样式只可定义作用域明确的局部 token，不得重定义全局 token 的语义或复制一套近似颜色。
- 组件和 workspace CSS 必须通过 `var(--app-*)` 消费语义 token，不得硬编码受主题影响的颜色、复制 token 的字面值，也不得把已有 token 挪作无关用途。只有新的语义角色能够跨场景复用时才增加 token。
- 所有主题必须维持相同的语义 token 契约：主题选择器只覆盖值，组件规则保持主题无关。新增、重命名 token 时必须同步更新所有主题变体和调用位置。
- Element Plus 的 `--el-*` 变量在共享主题边界中从 `--app-*` 映射，例如 `--el-color-primary: var(--app-accent)`；除非 Element Plus 没有可用的共享映射并在局部写明原因，否则禁止页面级零散覆盖 `--el-*`。

### 输入、搜索与筛选

- 查询框优先复用 `AppSearchField`，筛选入口优先复用 `AppFilterButton`；不在页面内复制近似的 HTML/CSS。
- 输入框必须使用全局 `--app-input-*`、`--app-border*`、`--app-accent*` token：默认边框保持浅色，focus 使用主题色和浅 focus ring，不能出现突兀深色边框。
- 标准搜索框高度由 `--app-search-field-height` 控制（当前默认 `28px`）。相邻筛选/图标按钮必须与当前工具栏的搜索控件视觉等高、对齐一致；不要各页面自行设置一组大尺寸。
- 图标按钮必须有 `title` 或 `aria-label`，并具备 hover、focus-visible、active、disabled 状态。长文本要处理 `min-width: 0`、截断和必要 tooltip。

## 4. AI、数据库与原生高风险能力

- AI/ACP/MCP 必须通过 Go 后端与后端事件处理流式输出、取消、审批、错误和重连。AI 工具调用、shell、文件、网络与写操作不得绕过确认策略。
- SQL 补全应由 `domain/database/` 的纯逻辑生成，融合关键词/模板与可用 schema、当前表字段、结果字段；缺少 schema 时仍须可用，且不能阻塞编辑器。为纯 completion/filter helper 保留可复现的人工验证场景。
- 数据库/Redis 的连接和会话归后端所有；明确区分只读预览与写入操作，支持取消长查询，展示真实错误。
- 终端/SSH/SFTP 保持资源标识稳定，避免响应式更新重启 PTY；订阅和会话必须有可预测的清理路径。远端输出不能作为可信 HTML 渲染。
- 文件、Git、HTTP 的路径/URL/命令等输入需要在后端再校验；前端验证只用于改善体验，不能替代安全边界。

## 5. 验证与交付

迭代期间运行最窄的相关静态检查；交付前按变更范围执行：

```bash
# 所有 Vue/TS 变更的最低要求
pnpm --dir vue-app lint
pnpm --dir vue-app typecheck

# 视图、样式、路由、共享组件、service、store、构建配置改动
pnpm --dir vue-app build

# 共享组件 / 组合式函数交互改动（Vitest + @vue/test-utils，见 docs/FRONTEND_STANDARDS.md 9.2）
pnpm --dir vue-app test:components

# 修改 Go 契约时
cd .. && go build ./...

# 最终 diff 检查
git diff --check
```

涉及 desktop IPC、持久化、终端/SSH、数据库、文件、流式 AI 或窗口事件时，还必须在 桌面端完成一次真实 smoke test；浏览器预览不构成原生集成验收。

交付说明必须列出：变更文件和用户可见行为、复用/抽取点、实际运行的检查及结果、未验证项或已知限制。不要覆盖、暂存或提交工作区中不属于当前任务的改动。

### 插件 UI 专项约束

`src/plugin-ui/` 的新增与维护必须遵守[插件 UI 规范](../docs/development/plugin-development.md#0-插件-ui-规范)：仅使用 Vue 3 + Element Plus 基础业务组件，表单使用 `ElForm/ElFormItem`，普通滚动容器使用 `ElScrollbar`。禁止原生控件替代、第二套 UI 库及绕过 iframe 宿主桥；修改旧插件 UI 时同步迁移页面中的违规控件，构建生成包内资源后执行对应测试。
