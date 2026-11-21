import { frameworkSuggestions } from "@/domain/editor/frameworks";
import { frameworksForPath, type FrameworkRegistration } from "@/services/editor-framework-registry";
import type { MonacoApi } from "@/services/monaco-contributions";
import type { editor } from "@/services/monaco-runtime.js";

export function frameworkModelPath(model: editor.ITextModel): string {
  if (model.uri.scheme !== "file") return "";
  return model.uri.path.replace(/^\/([A-Za-z]:\/)/, "$1");
}

/** 仅消费数据；不启动插件进程，悬停文案不允许 HTML 或受信任命令。 */
export function attachMonacoFrameworks(monaco: MonacoApi, registrations: readonly FrameworkRegistration[]): () => void {
  const languages = new Set(registrations.flatMap((item) => item.framework.languages));
  const providers: { dispose(): void }[] = [];
  for (const language of languages) {
    providers.push(monaco.languages.registerCompletionItemProvider(language, {
      triggerCharacters: [".", "<", " ", "@", ":"],
      provideCompletionItems(model, position) {
        const before = model.getLineContent(position.lineNumber).slice(0, position.column - 1);
        // 嵌套根或多种布局规则可能同时命中同一文件，相同补全只保留第一条。
        const seen = new Set<string>();
        const suggestions = frameworksForPath(frameworkModelPath(model)).filter((item) => item.framework.languages.includes(language))
          .flatMap(({ framework }) => frameworkSuggestions(framework, before))
          .filter((item) => {
            const key = `${item.label}\u0000${item.insertText}`;
            if (seen.has(key)) return false;
            seen.add(key);
            return true;
          })
          .map((item) => {
            const typed = before.slice(before.lastIndexOf(item.context) + item.context.length);
            return {
              label: item.label, insertText: item.insertText, detail: item.detail, documentation: item.documentation,
              kind: monaco.languages.CompletionItemKind.Function, sortText: "~framework",
              range: { startLineNumber: position.lineNumber, endLineNumber: position.lineNumber, startColumn: position.column - typed.length, endColumn: position.column },
            };
          }).slice(0, 100);
        return { suggestions };
      },
    }));
    providers.push(monaco.languages.registerHoverProvider(language, {
      provideHover(model, position) {
        const line = model.getLineContent(position.lineNumber);
        for (const { framework } of frameworksForPath(frameworkModelPath(model))) {
          if (!framework.languages.includes(language)) continue;
          for (const item of framework.completions) {
            const start = line.indexOf(item.label);
            if (start < 0 || position.column < start + 1 || position.column > start + item.label.length + 1) continue;
            const text = `${item.detail}\n\n${item.documentation}`.replace(/[\\`*_{}[\]()#+.!<>]/g, "\\$&");
            return { contents: [{ value: text, isTrusted: false, supportHtml: false }] };
          }
        }
        return null;
      },
    }));
  }
  return () => providers.forEach((provider) => provider.dispose());
}
