// Loaded only by the native -tags e2e smoke runner. Drives the real linter
// project prompt: host detection of the project's tools, the host-written
// dialog on opening a file, the preference and workspace setting it writes,
// and real `php -l` markers through the installed external-linters plugin.
import * as monaco from "@/services/monaco-runtime.js";
import { ensureMonacoContributions } from "@/services/monaco-contributions";
import { activeSmokeDialog } from "@/services/e2e-dialogs";
import { getPluginConfiguration } from "@/services/plugin-configuration-service";
import { installLocalPlugin, setPluginEnabled, uninstallPlugin } from "@/services/plugin-service";
import { invokeBackend } from "@/services/desktop-bridge";
import { useLocalProjectStore } from "@/stores/local-project";
import { usePluginLintersStore } from "@/stores/plugin-linters";
import { useWorkbenchStore } from "@/stores/workbench";
import type { SmokeConfig } from "@/domain/e2e/desktop-smoke";

const PLUGIN = "external-linters";

async function until<T>(read: () => T | undefined | false | null | Promise<T | undefined | false | null>, label: string, timeoutMs = 20_000): Promise<T> {
  const deadline = performance.now() + timeoutMs;
  while (performance.now() < deadline) {
    const value = await read();
    if (value) return value;
    await new Promise<void>((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`linters smoke timed out: ${label}`);
}

export async function lintersScenario(config: SmokeConfig, log: { done(step: string): void }) {
  const fixture = config.linters!;
  const store = usePluginLintersStore();
  const models: monaco.editor.ITextModel[] = [];
  let clicker: ReturnType<typeof setInterval> | undefined;
  await installLocalPlugin(fixture.packagePath);
  try {
    await setPluginEnabled(PLUGIN, true);
    await until(() => store.linters.filter((linter) => linter.pluginId === PLUGIN).length === 13, "13 linters registered");
    log.done("13 external-linters linters registered (contract limit 16, none dropped)");

    useLocalProjectStore().projectRoot = fixture.projectRoot;
    const detected = await until(async () => {
      const result = await invokeBackend<{ root: string; linters: string[] }>("plugin_host_linters_detect", {});
      return result.linters.includes(`${PLUGIN}/php`) && result.linters.includes(`${PLUGIN}/eslint`) ? result : undefined;
    }, "host detection of composer.json and web/eslint.config.js");
    if (detected.linters.includes(`${PLUGIN}/markdownlint`)) throw new Error("detected a tool the project does not use");
    log.done(`host detected ${detected.linters.join(", ")} in the project root and web/`);

    ensureMonacoContributions(monaco);
    let prompts = 0;
    clicker = setInterval(() => {
      const button = activeSmokeDialog(document)?.querySelector<HTMLButtonElement>(".el-message-box__btns .el-button--primary");
      if (button) { prompts += 1; button.click(); }
    }, 80);
    const php = monaco.editor.createModel("<?php\n$a = 1\necho $a;\n", "php", monaco.Uri.file(`${fixture.projectRoot}/src/a.php`));
    models.push(php);
    await until(() => store.selectedFor("php")?.ref === `${PLUGIN}/php`, "PHP linter selected through the prompt");
    const marker = await until(() => monaco.editor.getModelMarkers({ resource: php.uri }).find((item) => item.startLineNumber === 3 && /unexpected/.test(item.message)), "php -l marker on line 3");
    log.done(`opening a .php file asked once, enabling selected the PHP linter and real php -l marked line 3 (${marker.message})`);

    const ts = monaco.editor.createModel("export const value = 1;\n", "typescript", monaco.Uri.file(`${fixture.projectRoot}/web/a.ts`));
    models.push(ts);
    await until(() => store.selectedFor("typescript")?.ref === `${PLUGIN}/eslint`, "ESLint selected through the prompt");
    if (store.selectedFor("javascript")?.ref !== `${PLUGIN}/eslint` || store.selectedFor("vue")?.ref !== `${PLUGIN}/eslint`) throw new Error("ESLint not selected for every language");
    const settings = await getPluginConfiguration(PLUGIN, useWorkbenchStore().activeWorkspaceId);
    if (settings.effective["eslint.enabled"] !== true || settings.workspace["eslint.enabled"] !== true) throw new Error("eslint.enabled not turned on for the workspace");
    if (settings.effective["stylelint.enabled"] === true) throw new Error("an unrelated setting was turned on");
    log.done("opening a .ts file asked once and turned on ESLint for JavaScript, TypeScript and Vue plus this workspace's eslint.enabled");

    if (prompts !== 2) throw new Error(`expected 2 prompts, saw ${prompts}`);
    const js = monaco.editor.createModel("export const other = 2;\n", "javascript", monaco.Uri.file(`${fixture.projectRoot}/web/b.js`));
    models.push(js);
    const php2 = monaco.editor.createModel("<?php\necho 1;\n", "php", monaco.Uri.file(`${fixture.projectRoot}/src/b.php`));
    models.push(php2);
    await new Promise<void>((resolve) => setTimeout(resolve, 1500));
    if (prompts !== 2) throw new Error("asked again after the answer");
    log.done("later files of the same tools were not asked again");
  } finally {
    if (clicker) clearInterval(clicker);
    for (const model of models) model.dispose();
    await uninstallPlugin(PLUGIN).catch(() => undefined);
  }
}
