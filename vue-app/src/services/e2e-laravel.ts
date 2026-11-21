// Loaded only by the native -tags e2e smoke runner. Installs the packaged
// laravel-helper (and i18n-helper) plugins, opens a synthetic Laravel project
// through the real external-open path and drives the real Monaco editor:
// route/config/view/env completion, hover and go to definition, the Blade
// language and its tokens, Blade directive snippets, <x- component completion,
// the IDE Helper hint and translation keys in Blade.
import router from "@/router";
import * as monaco from "@/services/monaco-runtime.js";
import type { editor } from "@/services/monaco-runtime.js";
import { enqueueProjectFiles } from "@/services/external-file-open";
import { requestAiWorkspaceAction } from "@/services/ai-workspace-actions";
import { installLocalPlugin, setPluginEnabled, uninstallPlugin } from "@/services/plugin-service";
import { useLocalProjectStore } from "@/stores/local-project";
import { useWorkbenchStore } from "@/stores/workbench";
import type { SmokeConfig } from "@/domain/e2e/desktop-smoke";

const LARAVEL = "laravel-helper";
const I18N = "i18n-helper";

async function until<T>(read: () => T | undefined | false | null, label: string, timeoutMs: number): Promise<T> {
  const deadline = performance.now() + timeoutMs;
  while (performance.now() < deadline) {
    const value = read();
    if (value) return value;
    await new Promise<void>((resolve) => setTimeout(resolve, 80));
  }
  throw new Error(`laravel smoke timed out: ${label}`);
}

function modelFor(rel: string): editor.ITextModel | undefined {
  return monaco.editor.getModels().find((model) => model.uri.path.endsWith(`/${rel}`));
}

function editorFor(model: editor.ITextModel): editor.ICodeEditor | undefined {
  return monaco.editor.getEditors().find((item) => item.getModel() === model);
}

function visibleText(selector: string): string {
  return [...document.querySelectorAll<HTMLElement>(selector)]
    .filter((element) => element.offsetParent !== null)
    .map((element) => element.textContent ?? "")
    .join("\n");
}

function suggestionLabels(): string[] {
  return [...document.querySelectorAll<HTMLElement>(".suggest-widget .monaco-list-row")]
    .filter((row) => row.offsetParent !== null)
    .map((row) => (row.getAttribute("aria-label") ?? row.textContent ?? "").split(",")[0]!.trim());
}

/** 1-based position just after `needle` (plus `offset`) on the first line containing it. */
function after(model: editor.ITextModel, needle: string, offset = 0) {
  for (let line = 1; line <= model.getLineCount(); line++) {
    const index = model.getLineContent(line).indexOf(needle);
    if (index >= 0) return { lineNumber: line, column: index + needle.length + 1 + offset };
  }
  throw new Error(`"${needle}" not found`);
}

export async function laravelScenario(config: SmokeConfig, log: { done(step: string): void }) {
  const fixture = config.laravel!;
  const wait = config.timeoutMs;
  const project = useLocalProjectStore();
  const workbench = useWorkbenchStore();
  await installLocalPlugin(fixture.laravelPackage);
  await installLocalPlugin(fixture.i18nPackage);
  try {
    await setPluginEnabled(LARAVEL, true);
    await setPluginEnabled(I18N, true);
    log.done("laravel-helper and i18n-helper packages installed and enabled");

    const tab = workbench.openView("projects");
    if (!tab) throw new Error("Could not open project workspace");
    await router.push({ name: "projects" });
    enqueueProjectFiles([fixture.projectRoot]);
    await until(() => project.projectRoot === fixture.projectRoot, "Laravel project to open", wait);
    log.done("synthetic Laravel project opened through the real external-open path");

    // Opened the way a user opens a project file (command palette / tree): the
    // editor then gets the project root and a file: model URI.
    const openFile = async (rel: string) => {
      await requestAiWorkspaceAction("projects", { action: "open-file", projectRoot: fixture.projectRoot, path: rel });
      const model = await until(() => modelFor(rel), `${rel} Monaco model`, wait);
      const view = await until(() => editorFor(model), `${rel} editor`, wait);
      return { model, view };
    };

    // Plugin languages attach once the editor has loaded Monaco (the real
    // project editor calls ensureMonacoContributions); open a PHP file first.
    // The first file comes in the way the OS hands one over ("open with" /
    // drop): inside the open project it must open as a project document
    // (file: model URI), not as an external file without plugin features.
    enqueueProjectFiles([`${fixture.projectRoot}/app/Http/Controllers/ShowController.php`]);
    const incoming = await until(() => modelFor("app/Http/Controllers/ShowController.php"), "OS-opened ShowController.php model", wait);
    const incomingDocument = project.documents.find((item) => item.path.endsWith("/app/Http/Controllers/ShowController.php"));
    if (incoming.uri.scheme !== "file" || incomingDocument?.external) throw new Error(`OS-opened project file is external: uri=${incoming.uri.toString()} external=${String(incomingDocument?.external)}`);
    log.done("a project file handed over from outside opened as a project document (file: URI)");
    const php = await openFile("app/Http/Controllers/ShowController.php");
    await until(() => monaco.languages.getLanguages().some((language) => language.id === "blade"), "blade language registered", wait);
    log.done("Blade language registered from the plugin once the editor loaded Monaco");

    const hoverAt = async (view: editor.ICodeEditor, position: { lineNumber: number; column: number }, expect: (text: string) => boolean, label: string) => {
      view.setPosition(position);
      view.focus();
      await view.getAction("editor.action.showHover")?.run();
      const text = await until(() => {
        const value = visibleText(".monaco-hover");
        return expect(value) ? value : undefined;
      }, label, wait * 2);
      view.trigger("e2e-laravel", "editor.action.hideHover", null);
      return text.replace(/\s+/g, " ").trim();
    };

    const completeAt = async (view: editor.ICodeEditor, model: editor.ITextModel, line: number, text: string, expect: (labels: string[]) => boolean, label: string) => {
      const column = model.getLineMaxColumn(line);
      const before = model.getValue();
      view.executeEdits("e2e-laravel", [{ range: new monaco.Range(line, column, line, column), text: `\n${text}` }]);
      view.setPosition({ lineNumber: line + 1, column: model.getLineMaxColumn(line + 1) - (text.endsWith("')") ? 2 : 0) });
      view.focus();
      await view.getAction("editor.action.triggerSuggest")?.run();
      const position = view.getPosition()!;
      // The widget occasionally opens empty or not at all on the first trigger
      // while providers settle; re-trigger about once a second.
      let polls = 0;
      const labels = await until(() => {
        const rows = suggestionLabels();
        if (expect(rows)) return rows;
        if (++polls % 12 === 0) {
          view.focus();
          void view.getAction("editor.action.triggerSuggest")?.run();
        }
        return undefined;
      }, label, wait * 2).catch(async (error: unknown) => {
        // Evidence for the failure: what the widget showed and what the
        // registered completion providers return for the same position.
        const commands = (view as unknown as { _commandService?: { executeCommand(id: string, ...args: unknown[]): Promise<unknown> } })._commandService;
        const direct = await commands?.executeCommand("_executeCompletionItemProvider", model.uri, position).catch((reason: unknown) => `error: ${String(reason)}`);
        const directLabels = (direct as { suggestions?: { label: unknown }[] } | undefined)?.suggestions?.slice(0, 12).map((item) => typeof item.label === "string" ? item.label : JSON.stringify(item.label));
        // Same request straight to the plugin through the host, plus the host scope.
        const { requestCompletion } = await import("@/services/plugin-language-feature-service");
        const { currentPluginWorkspaceScope } = await import("@/services/plugin-workspace-scope");
        const relativePath = model.uri.path.startsWith(`${fixture.projectRoot}/`) ? model.uri.path.slice(fixture.projectRoot.length + 1) : undefined;
        const native = await requestCompletion({ pluginId: LARAVEL, id: "laravel-php" }, {
          languageId: model.getLanguageId(), fileName: model.uri.path.split("/").pop() ?? "", ...(relativePath ? { relativePath } : {}),
          text: model.getValue(), startLine: 1, position: { line: position.lineNumber, column: position.column }, locale: "zh",
        }).catch((reason: unknown) => `error: ${String(reason)}`);
        const scope = currentPluginWorkspaceScope();
        throw new Error(`${String(error)}; language=${model.getLanguageId()} uri=${model.uri.path} rel=${relativePath} line=${JSON.stringify(model.getLineContent(position.lineNumber))} col=${position.column} widget=${JSON.stringify(suggestionLabels().slice(0, 12))} providers=${JSON.stringify(directLabels ?? direct)} native=${JSON.stringify(native).slice(0, 400)} scope=${JSON.stringify(scope)} projectRoot=${fixture.projectRoot}`);
      });
      view.trigger("e2e-laravel", "hideSuggestWidget", null);
      // Remove exactly the inserted line (undo can merge with other edits).
      view.executeEdits("e2e-laravel", [{ range: new monaco.Range(line, column, line + 1, model.getLineMaxColumn(line + 1)), text: "" }]);
      if (model.getValue() !== before) throw new Error(`${label}: cleanup left ${JSON.stringify(model.getLineContent(line + 1))} on line ${line + 1}`);
      return labels;
    };

    // ---- Phase 1 in PHP ----
    // First ask the plugin directly through the host (same command the editor
    // uses), so a failure can be told apart from an editor wiring problem.
    {
      const { requestCompletion, requestHover } = await import("@/services/plugin-language-feature-service");
      const { currentPluginWorkspaceScope } = await import("@/services/plugin-workspace-scope");
      const rel = "app/Http/Controllers/ShowController.php";
      const at = after(php.model, "route('home", -1);
      const base = { languageId: "php", fileName: "ShowController.php", relativePath: rel, text: php.model.getValue(), startLine: 1, locale: "zh" };
      const hover = await requestHover({ pluginId: LARAVEL, id: "laravel-php" }, { ...base, position: { line: at.lineNumber, column: at.column } });
      const typed = after(php.model, "route('", 0);
      const completion = await requestCompletion({ pluginId: LARAVEL, id: "laravel-php" }, { ...base, position: { line: typed.lineNumber, column: typed.column } });
      log.done(`direct host probe: scope=${JSON.stringify(currentPluginWorkspaceScope())} hover=${JSON.stringify(hover).slice(0, 240)} completion=${JSON.stringify(completion).slice(0, 240)}`);
      // Editor layer: the providers Monaco runs for this exact model and position.
      const commands = (php.view as unknown as { _commandService?: { executeCommand(id: string, ...args: unknown[]): Promise<unknown> } })._commandService;
      const editorHover = await commands?.executeCommand("_executeHoverProvider", php.model.uri, at).catch((reason: unknown) => `error: ${String(reason)}`);
      const editorCompletion = await commands?.executeCommand("_executeCompletionItemProvider", php.model.uri, typed).catch((reason: unknown) => `error: ${String(reason)}`);
      const completionLabels = (editorCompletion as { suggestions?: { label: unknown }[] } | undefined)?.suggestions?.map((item) => typeof item.label === "string" ? item.label : JSON.stringify(item.label));
      const { useLocalProjectStore: projects } = await import("@/stores/local-project");
      log.done(`editor probe: uri=${php.model.uri.toString()} projects=${JSON.stringify(projects().projects.map((item) => item.root))} hover=${JSON.stringify(editorHover).slice(0, 240)} completion=${JSON.stringify(completionLabels ?? editorCompletion).slice(0, 240)}`);
    }
    const routes = await completeAt(php.view, php.model, 1, "route('admin.')", (rows) => rows.some((row) => row === "users" || row === "admin.users"), "route('admin.') completion");
    log.done(`route('admin.') completion offered ${routes.slice(0, 4).join(", ")}`);

    const routeHover = await hoverAt(php.view, after(php.model, "route('home", -1), (text) => text.includes("GET") && text.includes("routes/web.php"), "route('home') hover");
    log.done(`route('home') hover: ${routeHover.slice(0, 100)}`);

    php.view.setPosition(after(php.model, "route('home", -1));
    php.view.trigger("keyboard", "editor.action.revealDefinition", null);
    await until(() => project.activePath?.endsWith("/routes/web.php"), "route definition opened routes/web.php", wait);
    const webModel = await until(() => modelFor("routes/web.php"), "routes/web.php model", wait);
    const webEditor = await until(() => editorFor(webModel), "routes/web.php editor", wait);
    const routeLine = webEditor.getPosition()?.lineNumber ?? 0;
    if (!webModel.getLineContent(routeLine).includes("'home'")) throw new Error(`route definition landed on line ${routeLine}: ${webModel.getLineContent(routeLine)}`);
    log.done(`go to definition of route('home') opened routes/web.php line ${routeLine}`);

    const back = await openFile("app/Http/Controllers/ShowController.php");
    const configHover = await hoverAt(back.view, after(back.model, "config('app.na", -1), (text) => text.includes("config/app.php"), "config('app.name') hover");
    log.done(`config('app.name') hover: ${configHover.slice(0, 100)}`);
    const secretHover = await hoverAt(back.view, after(back.model, "config('services.stripe.sec", -1), (text) => text.includes("services.stripe.secret"), "config secret hover");
    if (secretHover.includes(fixture.canary)) throw new Error("secret config value leaked in hover");
    log.done("config('services.stripe.secret') hover masked the value");

    const configs = await completeAt(back.view, back.model, 1, "config('app.')", (rows) => rows.some((row) => row.includes("name")), "config('app.') completion");
    log.done(`config('app.') completion offered ${configs.slice(0, 4).join(", ")}`);

    const views = await completeAt(back.view, back.model, 1, "view('users.')", (rows) => rows.some((row) => row.includes("index")), "view('users.') completion");
    log.done(`view('users.') completion offered ${views.slice(0, 4).join(", ")}`);

    const envs = await completeAt(back.view, back.model, 1, "env('APP_')", (rows) => rows.some((row) => row.includes("APP_NAME")), "env('APP_') completion");
    if (envs.some((row) => row.includes("ONLY_IN_DOTENV"))) throw new Error(".env-only variable leaked into env() completion");
    log.done(`env('APP_') completion offered ${envs.slice(0, 4).join(", ")} without .env-only names`);

    const facade = await hoverAt(back.view, after(back.model, "Cache::g", -4), (text) => /IDE Helper/i.test(text), "Cache:: IDE Helper hint");
    if (facade.includes(fixture.canary)) throw new Error("hint leaked a secret");
    log.done(`Cache:: hover shows the IDE Helper hint: ${facade.slice(0, 80)}`);

    // ---- Phase 2 in Blade ----
    const blade = await openFile("resources/views/welcome.blade.php");
    if (blade.model.getLanguageId() !== "blade") throw new Error(`welcome.blade.php language is ${blade.model.getLanguageId()}`);
    log.done("welcome.blade.php opened with language blade");
    const tokens = monaco.editor.tokenize(blade.model.getValue(), "blade").flat().map((token) => token.type);
    if (!tokens.some((type) => type.includes("comment")) || !tokens.some((type) => type.includes("tag")) || !tokens.some((type) => type.includes("keyword"))) {
      throw new Error(`unexpected Blade tokens: ${[...new Set(tokens)].join(" ")}`);
    }
    log.done(`Blade tokenizer produced ${[...new Set(tokens)].length} distinct token types (comment, tag, keyword present)`);

    const snippets = await completeAt(blade.view, blade.model, 1, "@fore", (rows) => rows.some((row) => row.includes("@foreach")), "@foreach snippet");
    log.done(`Blade snippet completion offered ${snippets.filter((row) => row.startsWith("@")).slice(0, 3).join(", ")}`);

    const components = await completeAt(blade.view, blade.model, 1, "<x-", (rows) => rows.some((row) => row.includes("alert")), "<x- component completion");
    log.done(`<x- completion offered ${components.slice(0, 4).join(", ")}`);

    const bladeRoute = await hoverAt(blade.view, after(blade.model, "route('ho", 0), (text) => text.includes("routes/web.php"), "route() hover inside Blade");
    log.done(`route() hover works inside Blade: ${bladeRoute.slice(0, 60)}`);

    const directive = await hoverAt(blade.view, after(blade.model, "@for", 0), (text) => text.includes("@foreach") || /foreach/i.test(text), "@foreach directive hover");
    log.done(`@foreach directive hover: ${directive.slice(0, 60)}`);

    const translation = await hoverAt(blade.view, after(blade.model, "__('auth.fai", 0), (text) => text.includes("These credentials"), "__('auth.failed') hover in Blade (i18n-helper)");
    log.done(`i18n-helper hover in Blade: ${translation.slice(0, 80)}`);

    // Auto-closing: Monaco does not run auto-closing for programmatic "type"
    // commands (even "(" in built-in PHP stays unclosed), so the typing result is
    // a manual check (docs/verification/laravel-helper.md). Here: the Blade
    // configuration that carries the {{ / {{-- pairs was accepted by the host.
    const { monacoLanguageIssues, monacoPluginLanguageIds } = await import("@/services/monaco-languages");
    const bladeIssues = monacoLanguageIssues().filter((item) => item.language === "blade");
    if (!monacoPluginLanguageIds().includes("blade") || bladeIssues.length) throw new Error(`Blade language configuration issues: ${JSON.stringify(bladeIssues)}`);
    log.done("Blade language configuration (comments, {{ / {{-- auto-closing pairs, folding) accepted without issues");
  } finally {
    await setPluginEnabled(LARAVEL, false).catch(() => undefined);
    await setPluginEnabled(I18N, false).catch(() => undefined);
    await uninstallPlugin(LARAVEL, true).catch(() => undefined);
    await uninstallPlugin(I18N, true).catch(() => undefined);
  }
  log.done("plugins uninstalled with their data");
}
