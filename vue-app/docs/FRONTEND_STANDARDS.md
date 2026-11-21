# MinCode Vue App 前端开发规范

> **适用范围：** `vue-app/` 下所有 Vue 页面、组件、组合式函数、状态、服务、样式、文案及相关测试。
> **强制级别：** `MUST` 表示必须遵守；`SHOULD` 表示无明确理由不得偏离；`MAY` 表示可选。
> **生效方式：** `vue-app/AGENTS.md` 是面向自动化开发的强制入口；人工开发、代码评审和 PR 也以本文为准。

---

## 1. 技术基线与边界

| 项目       | 规范                                                                                                                                                                                                |
| ---------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 框架       | Vue 3 + TypeScript + Vite，使用 Composition API。                                                                                                                                                   |
| 组件库     | Element Plus；只允许在现有封装不能满足需求时扩展本项目组件。未经架构确认，不得再引入 Arco Vue、Ant Design Vue 等第二套基础组件库。                                                                  |
| 状态管理   | Pinia。                                                                                                                                                                                             |
| 桌面运行时 | tango + Go。                                                                                                                                                                                     |
| 样式       | Tailwind CSS v4 可用于局部布局；视觉 token 在 `src/styles/foundation/`，跨页面样式在 `src/styles/shared/`，feature 样式在 `src/styles/workspaces/<feature>/`；`src/styles.css` 只维护既定级联顺序。 |
| 路径别名   | 必须使用 `@/` 指向 `src/`，避免跨目录长相对路径。                                                                                                                                                   |
| 类型检查   | `strict: true`、`noUnusedLocals`、`noUnusedParameters` 已开启，代码必须保持通过。                                                                                                                   |

### 1.1 桌面桥是唯一原生边界

- **MUST** 通过 `src/services/desktop-bridge.ts` 调用 Go 后端：`invokeBackend<T>(command, args)`、`listenAppEvent`、`emitAppEvent` 与原生对话框封装。
- **MUST NOT** 新增 `@tauri-apps/*`、Tauri command、Rust 后端运行时依赖或浏览器端绕过原生边界的替代实现。
- **MUST** 在调用桌面专属能力前处理浏览器预览场景：使用 `isDesktopRuntime()`，提供明确但不阻断其余界面的降级状态。
- **MUST** 将请求/响应类型放在 `src/domain/<feature>/` 或紧邻对应 service，避免散落的 `Record<string, unknown>` 和未命名 JSON。

### 1.2 不以“能跑”为合格

以下做法禁止用于绕过问题：

- `any`、`@ts-ignore`、`@ts-nocheck`、关闭 TypeScript 检查；
- 不带原因的 `eslint-disable`、非空断言 `!`、吞掉异常的空 `catch`；
- 为修一个局部问题在全局 CSS 增加高优先级选择器、`!important` 或任意 `z-index`；
- 直接修改生成的命令清单（`src/generated/`）、`dist/` 产物，或把业务逻辑写进 `App.vue`；
- 为了快速实现新增第二套 UI 组件库。

确有例外时，必须在代码附近写出**原因、影响范围、移除条件**，并在交付说明中说明。

---

## 2. 目录与职责

```text
vue-app/src/
├── components/      # 可复用、可组合的业务/基础组件，不负责页面路由编排
├── composables/     # 可复用的响应式行为、订阅、交互状态
├── domain/          # 领域类型、纯转换、校验和业务规则
├── i18n/            # 语言词条和 i18n 访问入口
├── router/          # 路由定义、守卫和路由级元数据
├── services/        # 桌面桥接、持久化、网络/系统等外部副作用边界
├── stores/          # Pinia 跨组件/跨页面状态与业务 action
├── views/           # 路由或工作区级页面，负责组合而不是堆叠实现细节
├── styles/         # foundation、shared、workspaces/<feature> 的样式归属
└── styles.css       # 全局入口；保留明确的 CSS 级联导入顺序
```

### 2.1 放置规则

- **View：** 只做页面编排、路由参数衔接、顶层 layout 和 feature 入口；复杂表格、对话框、工具条应下沉到 `components/`。
- **Component：** 通过 props 输入、emits 输出；不得反向修改父组件状态，也不应直接依赖与自身无关的 store。
- **Composable：** 名称以 `use` 开头；负责可复用响应式逻辑，并在需要时暴露 `dispose`/停止函数。
- **Store：** 负责可持久化或跨树共享的领域状态、异步 action、加载/错误状态；不得把临时 hover、单个输入框焦点、Dialog 开关等纯局部状态无差别塞入 store。
- **Service：** 封装外部调用、序列化、持久化与浏览器/桌面差异；UI 组件不得自行散发同类 IPC 调用。
- **Domain：** 纯函数和类型优先放这里；不得引入 Vue、DOM、Element Plus 或桌面运行时。

---

## 3. Vue 与 TypeScript 规则

### 3.1 组件写法

- **MUST** 使用 `<script setup lang="ts">`，使用具名函数而不是匿名大函数堆在模板中。
- **MUST** 使用 `defineProps`、`withDefaults`、`defineEmits` 定义公共接口；props 和 emits 必须有明确类型。
- **SHOULD** 将复杂 template 条件、排序、过滤、派生数据收敛到 `computed`，不要在模板调用高开销表达式。
- **MUST NOT** 在模板中产生副作用（请求、写 store、写 localStorage、修改响应式对象）。
- **MUST** 使用稳定且领域唯一的 `:key`；禁止将数组索引用作可变列表的 key。
- **MUST** 在 `onBeforeUnmount` 中清理定时器、事件订阅、拖拽监听、`ResizeObserver`、原生资源及可取消请求。

```vue
<script setup lang="ts">
interface Props {
  connectionId: string;
  disabled?: boolean;
}

const props = withDefaults(defineProps<Props>(), { disabled: false });
const emit = defineEmits<{ open: [id: string]; refresh: [] }>();

function handleOpen() {
  if (!props.disabled) emit("open", props.connectionId);
}
</script>
```

### 3.2 响应式与数据流

- **MUST** 用 `ref` 管理标量和可替换对象，用 `computed` 表示派生状态；避免通过 `watch` 复制可由 `computed` 得出的状态。
- **SHOULD** 只 watch 明确来源；异步 watch 必须考虑快速切换导致的过期响应，必要时取消或比较请求标识。
- **MUST** 在异步 action 内维护 `loading`、错误提示及 finally 清理，不能让过期请求覆盖新数据。
- **MUST NOT** 直接修改传入 props、从组件外随意改写 store 的内部 ref，或用深层 mutation 掩盖状态边界。
- **MUST** 对后端数据做空值、权限、连接断开和运行时差异处理，不能假设所有字段永久存在。

### 3.3 类型和错误处理

- **MUST** 为 IPC、持久化、表单、列表和事件定义领域类型；优先 `unknown` + 类型收窄，而非 `any`。
- **MUST** 将用户可见错误转换为清楚、可行动的信息；日志或原始错误可保留给开发诊断，但不得直接把敏感内容渲染给用户。
- **MUST** 区分取消操作和失败操作。用户取消对话框不应显示“操作失败”。
- **SHOULD** 为 JSON/动态载荷提供守卫或解析函数，集中处理兼容性。

---

## 4. 状态、持久化与副作用

### 4.1 Pinia Store

每个 feature store 必须具备清晰边界：

- state：领域数据、加载状态、错误状态、当前选择；
- getters：只读派生信息；
- actions：加载、保存、连接、删除等异步业务行为；
- persistence：通过 `src/services/persistent-store.ts` 或 feature 级封装完成，并包含版本/兼容策略。

**禁止：** 把 IPC 调用分散在多个页面；把密码、Token、私钥、未保存文件内容等敏感数据写入普通持久化快照；把取消的请求当作错误 toast。

### 4.2 并发与生命周期

- 列表筛选、搜索、切换连接、切换 tab 等会连续触发请求的场景，必须防止旧请求回写新界面。
- 长连接、终端、SSE、事件订阅、窗口通信必须有明确的启动、恢复、关闭和清理路径。
- 需要用户确认的高风险操作（删除、覆盖、执行命令、连接外部资源）必须展示目标和影响，确认后再执行。

---

## 5. 国际化与文案

### 5.1 所有可见文本必须 i18n

- **MUST** 新增或修改用户可见文本时，通过 `src/i18n/messages.ts` 使用词条，并在 `src/i18n/locales/zh.ts` 与 `src/i18n/locales/en.ts` 同步维护对应内容。
- 包含：按钮、标题、空状态、占位符、表单标签、Tooltip、确认框、toast、错误提示、`aria-label`、快捷键描述和动态格式文本。
- **MUST** 使用语义化 key，例如 `database.redis.noKeys`、`common.save`；不得使用页面序号、中文句子或临时缩写作 key。
- **SHOULD** 复用已有 `common.*`、`nav.*` 等通用词条，避免产生同义重复。
- **MUST NOT** 在模板、script、service 中新增硬编码中文/英文用户文案。

```ts
const { t } = useVueI18n();
ElMessage.success(t("database.redis.saved"));
```

### 5.2 文案质量

- 使用明确动词：`保存 Key`、`重新连接`，不使用模糊的“确定”。
- 破坏性动作应说明对象：`删除 Redis Key “{name}”`。
- 空状态必须说明当前状态和下一步动作；加载状态不得与空数据状态混淆。

---

## 6. 视觉与交互：MinCode 紧凑工作台

项目使用 Element Plus，但视觉以 MinCode 的主题 token 为唯一来源：紧凑、清晰、主题自适应。不得依赖 Element Plus 默认配色、历史 Arco 主题名称或第二套设计语言作为新页面的视觉依据。

### 6.1 必须复用的视觉 token

优先使用 `src/styles/foundation/foundation.css` 定义的语义 token，并让 Element Plus 通过已有 `--el-*` 映射继承同一主题：

| 用途           | Token                                                    |
| -------------- | -------------------------------------------------------- |
| 主操作及焦点   | `--app-accent` / `--app-accent-hover`                    |
| 主操作浅色反馈 | `--app-accent-soft`                                      |
| 页面画布与面板 | `--app-canvas` / `--app-surface` / `--app-sidebar`       |
| 边框与 hover   | `--app-border` / `--app-border-strong` / `--app-hover`   |
| 文字           | `--app-text` / `--app-text-regular` / `--app-text-muted` |
| 输入控件       | `--app-input-*`、`--app-border*`、`--app-accent*`        |

- **MUST NOT** 在新 UI 中随意写近似主题色；确需新语义色时，先在 foundation 中定义语义变量，再在组件中使用。
- **MUST** 同时检查浅色和已有深色/主题模式。不得假设默认浅色值在所有主题下可读。
- **MUST NOT** 用大面积深色底表达普通 selected/active 状态；默认使用 `--app-accent-soft`、主题色文字或细主题色指示条。
- **MUST NOT** 用阴影表达普通层级。面板层级优先采用背景、边框、间距和状态变化。

### 6.2 组件、输入与状态

- **MUST** 先复用 `src/components/ui/AppSearchField.vue` 和 `src/components/ui/AppFilterButton.vue`，再考虑页面局部实现。相同模式出现在多个模块时，抽取为可配置公共组件、composable 或 shared style。
- 标准搜索框高度由 `--app-search-field-height` 控制（当前默认 `28px`）。相邻筛选、清除和图标按钮必须与所在工具栏搜索控件视觉等高、对齐一致；不得各页面自行做一组过高控件。
- 输入框默认边框必须保持浅色并使用 `--app-input-*` token；hover、focus、disabled、error 状态必须完整。focus 使用主题色和浅 focus ring，而不是突兀的深色描边。
- 普通表单和操作按钮使用当前 workspace 的紧凑密度与圆角；不要在同一工具栏混用多组高度、圆角和 padding。
- 选中列表使用浅色反馈和清晰指示；Tag 使用浅色背景、同色系文字和细边框；破坏性操作使用危险语义 token，默认不做实心高饱和危险主按钮。
- 图标按钮必须提供 `title` 或 `aria-label`；若带文字，不得继承固定正方形图标按钮尺寸。
- Loading、empty、disabled、error、offline 都必须有可辨识状态，不能只依赖颜色。

### 6.3 CSS 归属与规则

- **MUST** 按所有权放置样式：基础 token 在 `styles/foundation/`，跨 workspace 规则在 `styles/shared/`，业务样式在 `styles/workspaces/<feature>/`。详见 `src/styles/README.md`。
- **MUST** 保持 `src/styles.css` 的既定 import 顺序；它是级联契约，禁止按文件名排序或借重排解决优先级问题。
- **MUST** 优先复用已有 class、token 与 Element Plus 变量；功能组件可拥有 scoped 样式，但跨页面规则不可复制到各个 view。
- **MUST** 以 feature 前缀命名（例如 `redis-key-list`、`db-explorer__toolbar`），采用 BEM 风格 `block__element`、`block--modifier` 或 `is-*` 状态。
- **MUST NOT** 写无作用域的标签选择器影响业务 UI，如 `.el-button { ... }`、`button { ... }`，除非它属于已确认的 foundation 规则。
- **MUST NOT** 使用 `!important`；第三方组件覆盖冲突时，优先缩小上下文选择器、复用 CSS variables 或修正组件结构。确有例外时，必须在附近说明原因和影响范围。
- **SHOULD** 为超长名称设置 `min-width: 0`、ellipsis/tooltip，避免 key、路径、连接名挤坏布局。
- **MUST** 兼顾最小应用宽度和窄侧栏：长文案不得折成图标上方/下方，不得遮挡关闭、最大化等窗口控件。

### 6.4 可访问性与键盘操作

- 所有图标按钮必须有可访问名称；表单输入需有可关联的 label。
- 焦点态必须可见，使用 `--app-accent` 对应的细描边/浅色 focus ring，不能只禁用 outline。
- 自定义列表、菜单、tab、可拖拽分隔条须提供合理的键盘可达路径及 ARIA 语义。
- 颜色不能作为唯一信息来源；在线/离线、成功/失败需同时有图标、文字或状态描述。

## 7. 性能与数据密集界面

- 大表格、Redis Key、日志、文件树、终端输出等大量数据必须分页、虚拟化或增量加载；不得一次性渲染无上限列表。
- 搜索输入需要按场景做 debounce；切换连接/路由时取消或忽略旧结果。
- 不在 render/template 中反复 `JSON.stringify`、排序大数组、扫描整棵树或创建匿名回调链。
- Monaco、Xterm 等重组件按需挂载，保持稳定 key，避免无关响应式更新导致重建。
- 只有在证据表明需要时才使用缓存、`shallowRef` 或手动性能优化；优化后必须保持可维护性。

---

## 8. 安全与数据保护

- 严禁把密码、Token、私钥、连接字符串明文写入源码、日志、普通 localStorage、截图或错误提示。
- 使用现有加密/持久化边界处理敏感字段；持久化 schema 变化必须考虑老数据、缺失字段和回滚。
- 展示来自数据库、终端、HTTP 响应、文件系统的内容时，按不可信输入处理；禁止 `v-html`，除非经过明确消毒且有必要说明。
- 删除、覆盖、执行命令、批量修改、外部连接等高风险动作必须可取消、可解释且显示作用对象。

---

## 9. 检查、构建与交付

### 9.1 最低验证门槛

| 变更类型                                          | 必须执行                                                             |
| ------------------------------------------------- | -------------------------------------------------------------------- |
| 任意 Vue/TS 改动                                  | `pnpm --dir vue-app lint` + `pnpm --dir vue-app typecheck`           |
| 视图、样式、路由、共享组件、服务、store、构建配置 | 上述命令 + `pnpm --dir vue-app build`                                |
| 修改 Go API 契约                            | 上述命令 + `cd .. && go build ./...`                           |
| 修复纯函数/数据转换 bug                           | 写出并执行可复现验证步骤。                                            |
| 修改共享组件、组合式函数或其交互逻辑              | 上述命令 + `pnpm --dir vue-app test:components`，并为修复的 bug 补回归用例 |

构建产生的 chunk-size warning 若为既有告警可说明，但不得把 TypeScript 错误、构建失败或新增 warning 当作通过。

### 9.2 组件测试

- `tests/components/**/*.spec.ts` 使用 Vitest + `@vue/test-utils` + happy-dom 挂载真实 SFC，Element Plus 组件按需解析，与应用一致；配置见 `vitest.config.ts`。
- `tests/components/setup.ts` 为每个用例提供全新的 Pinia 并清空 `localStorage`；组件测试里没有 `window.tango`，按浏览器预览模式运行。需要原生能力时用 `vi.mock` 模拟对应 service，不要真实调用桌面宿主。
- 测试用户可见行为（渲染、ARIA、emit、store 变化），不要断言内部实现；Teleport 到 `body` 的内容（Drawer/Dialog）需 `attachTo: document.body` 后从 `document.body` 查询。
- 组合式函数通过最小宿主组件 `defineComponent({ setup() { ... } })` 挂载测试；`ElMessageBox`/`ElMessage` 用 `vi.mock("element-plus", ...)` 局部替换。
- `pnpm --dir vue-app test:components` 先用 `tsconfig.vitest.json` 做类型检查再运行用例；`scripts/*.test.mjs`（`pnpm test`）仍负责纯函数与源码契约测试。

### 9.3 视觉验收

涉及界面时，除编译外还必须逐项检查：

1. 正常、hover、focus、active、disabled、loading、empty、error 状态；
2. 长文本、窄侧栏、滚动、不同连接/无数据场景；
3. 图标与文本不重叠、不折行错位，按钮高度和对齐一致；
4. 选中态、Tag、边框、画布和文字颜色符合第 6 节；
5. 原生桌面运行时及浏览器预览的降级行为（如适用）。

### 9.4 交付说明格式

每次完成变更应简短说明：

- 修改的功能与关键文件；
- 用户可见的行为/视觉变化；
- 已执行的验证命令和结果；
- 未验证项、既有告警或需要人工确认的事项。

---

## 10. Code Review 清单

提交或评审前逐项确认：

- [ ] 组件使用 `<script setup lang="ts">`，公共 props/emits 和异步返回值有类型。
- [ ] 没有新增 `any`、忽略 TypeScript、Tauri API、生成物修改或敏感数据落盘。
- [ ] 用户可见文本已补齐 `zh` 与 `en` i18n 词条。
- [ ] IPC 调用集中在 service/store，且处理浏览器预览与失败场景。
- [ ] 状态归属合理；请求无旧结果覆盖新结果、订阅可清理。
- [ ] UI 复用 MinCode 主题 token 和公共控件；选中态、Tag、按钮、边框、焦点态一致，且输入/筛选控件高度匹配。
- [ ] 图标按钮有可访问名称，长文本和窄布局不会破坏界面。
- [ ] 大数据列表具备分页/虚拟化/增量策略，未在模板进行重计算。
- [ ] 已运行匹配第 9 节的 typecheck/build/test，并记录结果。
