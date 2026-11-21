import { spawnSync } from "node:child_process";
import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import tailwindcss from "@tailwindcss/vite";
import Components from "unplugin-vue-components/vite";
import { ElementPlusResolver } from "unplugin-vue-components/resolvers";
import ElementPlus from "unplugin-element-plus/vite";
import { visualizer } from "rollup-plugin-visualizer";


const elementPlusDevStyles = [
  "alert",
  "autocomplete",
  "badge",
  "button",
  "button-group",
  "card",
  "checkbox",
  "checkbox-group",
  "collapse",
  "collapse-item",
  "color-picker",
  "date-picker",
  "descriptions",
  "descriptions-item",
  "dialog",
  "drawer",
  "dropdown",
  "dropdown-item",
  "dropdown-menu",
  "empty",
  "form",
  "form-item",
  "icon",
  "input",
  "input-number",
  "input-otp",
  "input-tag",
  "loading",
  "menu",
  "menu-item",
  "menu-item-group",
  "message",
  "message-box",
  "notification",
  "option",
  "option-group",
  "popconfirm",
  "popover",
  "progress",
  "radio",
  "radio-group",
  "scrollbar",
  "segmented",
  "select",
  "skeleton",
  "slider",
  "splitter",
  "splitter-panel",
  "switch",
  "tab-pane",
  "table",
  "table-column",
  "table-v2",
  "tabs",
  "tag",
  "time-picker",
  "tooltip",
  "timeline",
  "timeline-item",
  "tree",
  "tree-select",
].map((component) => `element-plus/es/components/${component}/style/css`);


export default defineConfig({
  plugins: [
    {
      name: "mincode-xterm-keyboard-check",
      apply: "build",
      buildStart() {
        const testFile = fileURLToPath(new URL("./scripts/xterm-keyboard-input.test.mjs", import.meta.url));
        const result = spawnSync(process.execPath, ["--test", testFile], { stdio: "inherit" });
        if (result.error) throw result.error;
        if (result.status !== 0) {
          this.error("xterm keyboard regression check failed. Run pnpm install --frozen-lockfile from the repository root.");
        }
      },
    },
    vue(),
    Components({ resolvers: [ElementPlusResolver()] }),
    ElementPlus({}),
    tailwindcss(),
    process.env.ANALYZE === "true" &&
      visualizer({ filename: "dist/stats.html", template: "treemap", gzipSize: true }),
  ],
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
      // monaco-vim (lazy Vim editor mode, services/vim/monaco-vim-runtime.ts):
      // its "browser" export is a UMD build that bundles a second, older Monaco,
      // so use the ESM build; its deep `monaco-editor/esm/vs/...` imports are not
      // reachable through monaco-editor 0.56's exports map, so point them at the
      // same files monaco-runtime.js imports (one Monaco instance).
      "monaco-vim": fileURLToPath(new URL("./node_modules/monaco-vim/dist/index.mjs", import.meta.url)),
      "monaco-editor/esm/vs": fileURLToPath(new URL("./node_modules/monaco-editor/esm/vs", import.meta.url)),
    },
    dedupe: ["vue"],
  },
  server: {
    host: "127.0.0.1",
    port: Number(process.env.VITE_PORT) || 5150,
    strictPort: true,
    // tango 宿主以 tango://app 代理到本服务器，请求 Host 为 "app"，
    // 需放行否则 Vite 的 DNS-rebinding 防护会拦截（Blocked request）。
    allowedHosts: ["app", "localhost"],
    // The desktop app loads the page from tango://app (no host or port Vite can reach) and
    // proxies to this server, so the HMR client must dial the dev server itself.
    hmr: { host: "127.0.0.1", port: Number(process.env.VITE_PORT) || 5150 },
  },
  optimizeDeps: {
    // Settings, AI and most workspaces are lazy routes/components. Prebundle every
    // Element Plus style entrypoint they use so the desktop WebView is not reloaded the
    // first time a lazy feature opens. Vue is deduped above to keep one runtime.
    entries: [
      "src/main.ts",
      "src/App.vue",
      "src/views/**/*.vue",
      "src/components/**/*.vue",
    ],
    include: [
      "element-plus/es",
      "element-plus/es/components/base/style/css",
      // pnpm 严格模式不提升 @vue/shared 到顶层 node_modules，element-plus 预 bundle
      // 产物对它的裸导入会解析失败；纳入预 bundle 使其可解析。
      "@vue/shared",
      ...elementPlusDevStyles,
    ],
    // Prebundling monaco-vim would inline a private copy of the Monaco modules it imports.
    exclude: ["monaco-vim"],
  },
  build: {
    // 桌面宿主（../main.go）以 //go:embed 嵌入 ../frontend/dist；VUE_OUTDIR 可改输出目录。
    outDir: process.env.VUE_OUTDIR ?? "../frontend/dist",
    emptyOutDir: true,
    // WKWebView 原生支持 link rel=modulepreload，无需注入 polyfill shim，
    // 减少首屏内联脚本与运行时 link 处理开销。
    modulePreload: { polyfill: false },
    // The synchronous subscriber must be ready before startup IPC; keep this small
    // feature entry separate from the shell, and its dialog/state/parser lazy.
    rolldownOptions: {
      output: {
        codeSplitting: {
          groups: [{
            name: "plugin-host-listeners",
            test: /[/\\]services[/\\]plugin-(?:publish-listener|host-ui)\.ts$/,
            includeDependenciesRecursively: false,
          }],
        },
      },
    },
    chunkSizeWarningLimit: 900,
  },
});
