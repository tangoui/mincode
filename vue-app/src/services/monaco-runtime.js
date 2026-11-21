import EditorWorker from "../../node_modules/monaco-editor/esm/vs/editor/editor.worker.js?worker";
import JsonWorker from "../../node_modules/monaco-editor/esm/vs/language/json/json.worker.js?worker";

// Register only the editor capabilities MinCode exposes. Avoid editor.main/index because
// those entrypoints eagerly include every Monaco language, worker, and optional feature.
import "../../node_modules/monaco-editor/esm/vs/features/bracketMatching/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/caretOperations/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/clipboard/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/codeAction/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/codeEditor/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/codelens/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/codicon/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/colorPicker/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/comment/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/contextmenu/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/cursorUndo/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/diffEditor/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/documentSymbols/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/dropOrPasteInto/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/find/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/folding/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/format/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/gotoError/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/gotoLine/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/gotoSymbol/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/hover/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/indentation/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/inlayHints/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/inlineCompletions/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/lineSelection/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/linesOperations/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/linkedEditing/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/links/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/multicursor/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/parameterHints/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/referenceSearch/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/rename/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/semanticTokens/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/smartSelect/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/snippet/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/stickyScroll/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/suggest/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/tokenization/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/unicodeHighlighter/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/wordHighlighter/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/wordOperations/register.js";
import "../../node_modules/monaco-editor/esm/vs/features/wordPartOperations/register.js";

// Syntax highlighting for the file types recognized by the project editor.
// Register every language definition bundled with Monaco. This adds file associations up front,
// while each tokenizer remains a lazy chunk that loads only when its language is opened.
import "../../node_modules/monaco-editor/esm/vs/languages/definitions/register.all.js";

import { languages as monacoLanguages } from "../../node_modules/monaco-editor/esm/vs/editor/editor.api.js";

// Monaco 不认识 .vue。曾经把它并入 `html`，但 SFC 的 <script lang="ts"> 是 TypeScript，
// 而 html-ls 会把 <script> 体当成内联 JavaScript 校验，于是 `import type` 这类纯 TS
// 语法被报成 TS5097「'import type' declarations can only be used in TypeScript files.」，
// 满屏红波浪线。给 .vue 一个独立语言 id，让它只匹配到懂 SFC 的 vue-language-server
// （Volar），不再落进 html-ls 的语言列表。
// 高亮沿用 HTML 的 Monarch 规则：SFC 外层就是标签结构，且该规则已支持 <script>/<style>
// 的嵌入着色，足够用且不额外增加打包体积。
monacoLanguages.register({
  id: "vue",
  extensions: [".vue"],
  aliases: ["Vue", "vue"],
  mimetypes: ["text/x-vue"],
});
monacoLanguages.registerTokensProviderFactory("vue", {
  create: async () => {
    const mod = await import("../../node_modules/monaco-editor/esm/vs/languages/definitions/html/html.js");
    return mod.language;
  },
});
monacoLanguages.onLanguageEncountered("vue", async () => {
  const mod = await import("../../node_modules/monaco-editor/esm/vs/languages/definitions/html/html.js");
  monacoLanguages.setLanguageConfiguration("vue", mod.conf);
});

// Monaco 没有内置 TOML：.toml 会被识别成 plaintext，taplo 这类 TOML LSP 永远匹配不到。
// 注册独立语言 id，并把高亮借给 ini 的 Monarch 规则——两者的 key = value、[section]、
// # 注释结构基本一致，足够用且不额外增加打包体积。
monacoLanguages.register({
  id: "toml",
  extensions: [".toml"],
  filenames: ["Cargo.lock", "Gopkg.lock", "Pipfile"],
  aliases: ["TOML", "toml"],
  mimetypes: ["text/x-toml"],
});
monacoLanguages.registerTokensProviderFactory("toml", {
  create: async () => {
    const mod = await import("../../node_modules/monaco-editor/esm/vs/languages/definitions/ini/ini.js");
    return mod.language;
  },
});
monacoLanguages.onLanguageEncountered("toml", async () => {
  const mod = await import("../../node_modules/monaco-editor/esm/vs/languages/definitions/ini/ini.js");
  monacoLanguages.setLanguageConfiguration("toml", mod.conf);
});

// Svelte SFC 与 .vue 同理：Monaco 不认识 .svelte，给它独立语言 id 才能路由到
// svelte-language-server；高亮沿用 HTML 的 Monarch 规则（外层就是标签结构）。
monacoLanguages.register({
  id: "svelte",
  extensions: [".svelte"],
  aliases: ["Svelte", "svelte"],
  mimetypes: ["text/x-svelte"],
});
monacoLanguages.registerTokensProviderFactory("svelte", {
  create: async () => {
    const mod = await import("../../node_modules/monaco-editor/esm/vs/languages/definitions/html/html.js");
    return mod.language;
  },
});
monacoLanguages.onLanguageEncountered("svelte", async () => {
  const mod = await import("../../node_modules/monaco-editor/esm/vs/languages/definitions/html/html.js");
  monacoLanguages.setLanguageConfiguration("svelte", mod.conf);
});

// Prisma schema（.prisma）同样不在 Monaco 内置语言里；先只注册语言 id 让
// prisma-language-server 能路由到，高亮暂用默认（不引入额外 tokenizer）。
monacoLanguages.register({
  id: "prisma",
  extensions: [".prisma"],
  aliases: ["Prisma", "prisma"],
  mimetypes: ["text/x-prisma"],
});

// Astro SFC 与 .vue/.svelte 同理：外层是标签结构，高亮复用 HTML 的 Monarch 规则。
monacoLanguages.register({
  id: "astro",
  extensions: [".astro"],
  aliases: ["Astro", "astro"],
  mimetypes: ["text/x-astro"],
});
monacoLanguages.registerTokensProviderFactory("astro", {
  create: async () => {
    const mod = await import("../../node_modules/monaco-editor/esm/vs/languages/definitions/html/html.js");
    return mod.language;
  },
});
monacoLanguages.onLanguageEncountered("astro", async () => {
  const mod = await import("../../node_modules/monaco-editor/esm/vs/languages/definitions/html/html.js");
  monacoLanguages.setLanguageConfiguration("astro", mod.conf);
});

// 以下语言 Monaco 未内置，只注册语言 id 供对应 LSP（zls/haskell-ls/ocamllsp）
// 路由；高亮暂用默认，不引入额外 tokenizer。
monacoLanguages.register({
  id: "zig",
  extensions: [".zig", ".zon"],
  aliases: ["Zig", "zig"],
});
monacoLanguages.register({
  id: "haskell",
  extensions: [".hs", ".lhs"],
  aliases: ["Haskell", "haskell"],
});
monacoLanguages.register({
  id: "ocaml",
  extensions: [".ml", ".mli"],
  aliases: ["OCaml", "ocaml"],
});

// Keep rich language services only where MinCode uses them today.
// TS/JS 富语言能力（补全/悬停/诊断等）统一由后端 LSP（typescript-language-server）
// 提供，因此不注册 Monaco 内置的 typescript contribution：高亮、括号、注释配置
// 来自上方 definitions/register.all.js 的 Monarch 规则，不依赖该 contribution。
// 不注册即可避免打包 6.9MB 的 ts.worker，也彻底消除内置 provider 与 LSP 双写的可能。
import "../../node_modules/monaco-editor/esm/vs/language/json/monaco.contribution.js";

const workerConstructors = {
  json: JsonWorker,
};

const monacoGlobal = globalThis;

monacoGlobal.MonacoEnvironment = {
  getWorker(_moduleId, label) {
    const WorkerConstructor = workerConstructors[label] ?? EditorWorker;
    return new WorkerConstructor();
  },
};

export * from "../../node_modules/monaco-editor/esm/vs/editor/editor.api.js";

// Seams for plugin contribution points (jsonValidation, formatters). jsonDefaults comes from
// the json contribution imported above, and FormattingConflicts from the same format module
// instance the format feature registered, so selectors installed here govern the editor's
// own Format Document / format-on-save choice.
export { jsonDefaults as mincodeJsonDefaults } from "../../node_modules/monaco-editor/esm/vs/language/json/monaco.contribution.js";
export { FormattingConflicts as mincodeFormattingConflicts } from "../../node_modules/monaco-editor/esm/vs/editor/contrib/format/browser/format.js";

// Seam for the Outline panel: merged document symbols from every registered provider
// (LSP bridges and Monaco's built-in JSON), the same model the editor's breadcrumbs use.
import { StandaloneServices } from "../../node_modules/monaco-editor/esm/vs/editor/standalone/browser/standaloneServices.js";
import { ILanguageFeaturesService } from "../../node_modules/monaco-editor/esm/vs/editor/common/services/languageFeatures.js";
import { OutlineModel } from "../../node_modules/monaco-editor/esm/vs/editor/contrib/documentSymbols/browser/outlineModel.js";

function documentSymbolRegistry() {
  return StandaloneServices.get(ILanguageFeaturesService).documentSymbolProvider;
}

export const mincodeDocumentSymbols = {
  has(model) {
    return documentSymbolRegistry().has(model);
  },
  async get(model, token) {
    const outline = await OutlineModel.create(documentSymbolRegistry(), model, token);
    return outline.getTopLevelSymbols();
  },
  onDidChange(listener) {
    return documentSymbolRegistry().onDidChange(() => listener());
  },
};

// A model-targeted, silent formatting seam; never runs an explicit editor action.
import { getRealAndSyntheticDocumentFormattersOrdered, formatDocumentWithProvider } from "../../node_modules/monaco-editor/esm/vs/editor/contrib/format/browser/format.js";
import { IInstantiationService } from "../../node_modules/monaco-editor/esm/vs/platform/instantiation/common/instantiation.js";
import { CancellationTokenSource } from "../../node_modules/monaco-editor/esm/vs/base/common/cancellation.js";

// Base selection also serves builtin-only format-on-paste/type. Plugin attachment adds its own selector on top.
import { FormattingConflicts } from "../../node_modules/monaco-editor/esm/vs/editor/contrib/format/browser/format.js";
FormattingConflicts.setFormatterSelector(async (providers) => providers[0]);

export const mincodeSaveFormatting = {
  async run(model, choose, signal) {
    const source = new CancellationTokenSource();
    const abort = () => source.cancel();
    if (signal.aborted) abort();
    signal.addEventListener("abort", abort, { once: true });
    const disposed = model.onWillDispose(abort);
    try {
      const service = StandaloneServices.get(ILanguageFeaturesService);
      const providers = getRealAndSyntheticDocumentFormattersOrdered(service.documentFormattingEditProvider, service.documentRangeFormattingEditProvider, model);
      const selected = choose(providers);
      if (!selected || source.token.isCancellationRequested || model.isDisposed()) return;
      model.pushStackElement();
      await StandaloneServices.get(IInstantiationService).invokeFunction(formatDocumentWithProvider, selected, model, 2, source.token, false);
      if (!model.isDisposed()) model.pushStackElement();
    } finally {
      source.cancel();
      source.dispose();
      disposed.dispose();
      signal.removeEventListener("abort", abort);
    }
  },
};

// Seam for submenus in the editor context menu (plugin services, monaco-menus.ts).
// addEditorAction only adds flat items that hide when their precondition fails;
// a submenu item keeps `when` (shown) apart from `precondition` (enabled), so an
// item can stay visible but disabled.
import { MenuId, MenuRegistry } from "../../node_modules/monaco-editor/esm/vs/platform/actions/common/actions.js";
import { ContextKeyExpr } from "../../node_modules/monaco-editor/esm/vs/platform/contextkey/common/contextkey.js";

/** MenuIds cannot be created twice; keep one per submenu id. */
const mincodeSubmenuIds = new Map();

function mincodeSubmenuId(id) {
  let menuId = mincodeSubmenuIds.get(id);
  if (!menuId) {
    menuId = new MenuId(`mincode.${id}`);
    mincodeSubmenuIds.set(id, menuId);
  }
  return menuId;
}

export const mincodeEditorContextMenu = {
  appendSubmenu({ id, title, group, order, when }) {
    return MenuRegistry.appendMenuItem(MenuId.EditorContext, {
      submenu: mincodeSubmenuId(id),
      title,
      group,
      order,
      when: ContextKeyExpr.deserialize(when),
    });
  },
  appendItem(submenu, { command, title, tooltip, when, precondition, order }) {
    return MenuRegistry.appendMenuItem(mincodeSubmenuId(submenu), {
      command: {
        id: command,
        title,
        ...(tooltip ? { tooltip } : {}),
        precondition: ContextKeyExpr.deserialize(precondition),
      },
      when: ContextKeyExpr.deserialize(when),
      order,
    });
  },
};
