import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vitest/config";
import vue from "@vitejs/plugin-vue";
import Components from "unplugin-vue-components/vite";
import { ElementPlusResolver } from "unplugin-vue-components/resolvers";

// Component tests mount real SFCs in happy-dom (a browser preview: no window.tango).
// The app's Tailwind plugin is intentionally omitted; Element Plus components resolve like in the app.
// Run through scripts/vitest.mjs (pnpm test:components) on Windows; see that file.
export default defineConfig({
  plugins: [
    vue(),
    Components({ resolvers: [ElementPlusResolver({ importStyle: false })], dts: false }),
  ],
  resolve: {
    alias: {
      "dompurify": fileURLToPath(new URL("./node_modules/dompurify/dist/purify.es.mjs", import.meta.url)),
      "marked": fileURLToPath(new URL("./node_modules/marked/lib/marked.esm.js", import.meta.url)),
      "@mincode/plugin-ui": fileURLToPath(new URL("./src/plugin-ui", import.meta.url)),
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
    dedupe: ["vue", "element-plus", "@element-plus/icons-vue"],
  },
  test: {
    // Each isolated worker loads Vue/Element Plus and a DOM. CPU-count defaults
    // oversubscribe these memory-heavy suites and starve real async work even
    // without another gate running. Bound concurrency, not test timeouts.
    maxWorkers: 2,
    environment: "happy-dom",
    include: ["tests/components/**/*.spec.ts", "tests/browser-scripts/**/*.spec.ts"],
    setupFiles: ["tests/components/setup.ts"],
    restoreMocks: true,
    server: { deps: { inline: ["element-plus"] } },
  },
});
